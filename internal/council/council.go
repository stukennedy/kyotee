// Package council implements multi-model consensus (spec 06): ≥3 models from
// distinct vendors debate until they converge or a round cap is hit, then a
// Synthesis stage collapses the debate into one answer.
package council

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"

	"github.com/stukennedy/kyotee/internal/budget"
	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/jsonx"
	"github.com/stukennedy/kyotee/internal/pipeline"
	"github.com/stukennedy/kyotee/internal/provider"
	"github.com/stukennedy/kyotee/internal/thinking"
)

// Embedder is the vendor-agnostic embedding surface for the similarity method.
type Embedder interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

type ConsensusConfig struct {
	Method    string  // "vote" | "similarity" | "judge"
	Threshold float64 // meaning depends on method
}

// Meta keys the council leaves for the Synthesis stage.
const (
	MetaOutcome = "council.outcome" // "consensus" | "referee" | "majority_vote" | "synthesis_notes_dissent" | "budget_halt"
	MetaDissent = "council.dissent" // summary of unresolved disagreement, if any
	MetaWinner  = "council.winner"  // referee/majority-selected position, if any
)

const maxRoundsHardCap = 5

type Stage struct {
	Members    []provider.Provider // SHOULD span >=2 vendors (warn if not)
	Rounds     int                 // default 3, hard-capped
	Consensus  ConsensusConfig
	OnDeadlock string            // "referee" | "majority_vote" | "synthesis_notes_dissent"
	Referee    provider.Provider // judge-method checks and referee deadlock resolution
	Embedder   Embedder          // similarity method
	Tools      *thinking.ToolRegistry
	MaxTokens  int
}

func (c *Stage) ID() string { return "council" }

// member holds one debater's evolving position.
type member struct {
	p        provider.Provider
	position string
	vote     vote
	hasVote  bool
}

type vote struct {
	Choice         string  `json:"choice"`
	Confidence     float64 `json:"confidence"`
	AgreeWithGroup bool    `json:"agree_with_group"`
}

func (c *Stage) Run(ctx context.Context, st *pipeline.State, emit events.Emitter) (*pipeline.State, error) {
	if len(c.Members) < 2 {
		return st, errors.New("council: needs at least 2 members")
	}
	rounds := c.Rounds
	if rounds <= 0 {
		rounds = 3
	}
	if rounds > maxRoundsHardCap {
		rounds = maxRoundsHardCap
	}
	method := c.Consensus.Method
	if method == "" {
		method = "vote"
	}

	c.warnIfSameVendor(emit)

	members := make([]*member, len(c.Members))
	for i, p := range c.Members {
		members[i] = &member{p: p}
	}

	// Opening turns run in parallel — independent, no shared context, no anchoring.
	var mu sync.Mutex
	var wg sync.WaitGroup
	openErrs := make([]error, len(members))
	for i, m := range members {
		wg.Add(1)
		go func(i int, m *member) {
			defer wg.Done()
			text, usage, err := c.memberCall(ctx, m.p, st, c.openingPrompt(st, method))
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				openErrs[i] = err
				return
			}
			m.position = text
			m.parseVote(method)
			st.AddTurn(c.ID(), "council:"+m.p.Name(), text, usage)
			budget.CheckWarn(&st.Budget, emit)
			emit(events.Event{
				Kind: events.KindCouncilOpening, Stage: c.ID(), Actor: m.p.Name(),
				Payload: map[string]any{"model": m.p.Name(), "position": text},
			})
		}(i, m)
	}
	wg.Wait()

	// Drop members whose opening failed; the debate continues if ≥2 survive.
	alive := members[:0]
	for i, m := range members {
		if openErrs[i] == nil {
			alive = append(alive, m)
		} else {
			emit(events.Event{
				Kind: events.KindError, Stage: c.ID(), Actor: members[i].p.Name(),
				Payload: map[string]any{"message": "opening failed: " + openErrs[i].Error(), "stage": c.ID()},
			})
		}
	}
	members = alive
	if len(members) < 2 {
		return st, errors.New("council: too few members survived openings")
	}

	// Rebuttal rounds until consensus, round cap, or budget halt.
	roundsUsed := 0
	consensusReached := false
	for r := 1; r <= rounds; r++ {
		if reached, summary := c.checkConsensus(ctx, st, members, method, emit, roundsUsed); reached {
			consensusReached = true
			st.Meta[MetaOutcome] = "consensus"
			if summary != "" {
				st.Meta[MetaWinner] = summary
			}
			break
		}
		if st.Budget.Exhausted() {
			st.Meta[MetaOutcome] = "budget_halt"
			st.Meta[MetaDissent] = c.positionDigest(members)
			emit(events.Event{
				Kind: events.KindCouncilConsensus, Stage: c.ID(),
				Payload: map[string]any{"reached": false, "method": "budget_halt", "rounds_used": roundsUsed},
			})
			return st, nil // Synthesis runs on whatever positions exist.
		}

		roundsUsed = r
		snapshot := c.positionSnapshot(members)
		var rwg sync.WaitGroup
		for _, m := range members {
			rwg.Add(1)
			go func(m *member) {
				defer rwg.Done()
				text, usage, err := c.memberCall(ctx, m.p, st, c.rebuttalPrompt(st, m, snapshot, r, method))
				mu.Lock()
				defer mu.Unlock()
				if err != nil {
					emit(events.Event{
						Kind: events.KindError, Stage: c.ID(), Actor: m.p.Name(),
						Payload: map[string]any{"message": "rebuttal failed: " + err.Error(), "stage": c.ID()},
					})
					return
				}
				m.position = text
				m.parseVote(method)
				st.AddTurn(c.ID(), "council:"+m.p.Name(), text, usage)
				budget.CheckWarn(&st.Budget, emit)
				emit(events.Event{
					Kind: events.KindCouncilRebuttal, Stage: c.ID(), Actor: m.p.Name(),
					Payload: map[string]any{"model": m.p.Name(), "round": r, "text": text},
				})
			}(m)
		}
		rwg.Wait()
	}

	if !consensusReached {
		if reached, summary := c.checkConsensus(ctx, st, members, method, emit, roundsUsed); reached {
			consensusReached = true
			st.Meta[MetaOutcome] = "consensus"
			if summary != "" {
				st.Meta[MetaWinner] = summary
			}
		}
	}

	if !consensusReached {
		c.resolveDeadlock(ctx, st, members, emit, roundsUsed)
	}
	return st, nil
}

// memberCall runs one debater turn, honoring thinking effort and flagged tools.
func (c *Stage) memberCall(ctx context.Context, p provider.Provider, st *pipeline.State, prompt string) (string, provider.Usage, error) {
	flagged := thinking.FlaggedTools(st)
	req := provider.Request{
		System: "You are one member of a council of AI models from different vendors debating a hard problem. Be substantive, concise, and willing to change your mind when another member's argument is better." +
			thinking.ToolInstruction(flagged),
		Messages:        []provider.Message{provider.UserText(prompt)},
		ReasoningEffort: thinking.SolverEffort(st),
		MaxTokens:       c.MaxTokens,
		Metadata:        map[string]string{"task_id": st.TaskID, "stage": c.ID(), "member": p.Name()},
	}
	if len(flagged) > 0 && c.Tools != nil {
		req.Tools = c.Tools.DefsFor(flagged)
	}
	resp, usage, err := thinking.RunToolLoop(ctx, p, req, c.Tools, 3, func(events.Event) {}, c.ID())
	if err != nil {
		return "", usage, err
	}
	return resp.Text(), usage, nil
}

const voteInstruction = `

End your reply with a single JSON object on its own line (no fences):
{"choice": "<short label for the answer/option you support>", "confidence": <0..1>, "agree_with_group": <bool>}`

func (c *Stage) openingPrompt(st *pipeline.State, method string) string {
	p := "Task:\n" + st.Original + "\n\nState your opening position: your answer and the key reasons for it."
	if method == "vote" {
		p += voteInstruction
	}
	return p
}

func (c *Stage) rebuttalPrompt(st *pipeline.State, m *member, others []string, round int, method string) string {
	p := fmt.Sprintf(`Task:
%s

Round %d. Your previous position:
%s

The other council members' latest positions (distilled):
%s

Rebut or revise. If another member's argument is stronger, adopt it and say so. State clearly whether you now agree with the group.`,
		st.Original, round, distill(m.position, 1200), strings.Join(others, "\n\n---\n\n"))
	if method == "vote" {
		p += voteInstruction
	}
	return p
}

// positionSnapshot distills every member's latest position for handoff —
// distilled envelopes, not full transcripts, keep per-round cost sub-linear.
func (c *Stage) positionSnapshot(members []*member) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, fmt.Sprintf("[%s]\n%s", m.p.Name(), distill(m.position, 800)))
	}
	return out
}

func (c *Stage) positionDigest(members []*member) string {
	return strings.Join(c.positionSnapshot(members), "\n\n")
}

func (m *member) parseVote(method string) {
	if method != "vote" {
		return
	}
	var v vote
	if err := jsonx.ParseLast(m.position, &v); err == nil && v.Choice != "" {
		m.vote = v
		m.hasVote = true
	}
}

// distill truncates on a sentence-ish boundary to bound handoff token cost.
func distill(s string, max int) string {
	s = strings.TrimSpace(s)
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	if i := strings.LastIndexAny(cut, ".\n"); i > max/2 {
		cut = cut[:i+1]
	}
	return cut + " […]"
}

func (c *Stage) warnIfSameVendor(emit events.Emitter) {
	vendors := map[string]bool{}
	for _, m := range c.Members {
		vendors[m.Vendor()] = true
	}
	if len(vendors) < 2 {
		names := make([]string, 0, len(c.Members))
		for _, m := range c.Members {
			names = append(names, m.Name())
		}
		sort.Strings(names)
		emit(events.Event{
			Kind: events.KindError, Stage: c.ID(),
			Payload: map[string]any{
				"level":   "warn",
				"message": "council members all share one vendor (" + strings.Join(names, ", ") + "); same-family models tend to agree — false consensus risk",
			},
		})
	}
}
