package council

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/jsonx"
	"github.com/stukennedy/kyotee/internal/pipeline"
	"github.com/stukennedy/kyotee/internal/provider"
)

// checkConsensus runs the configured detection method and emits
// council.vote (per member, vote method) and council.consensus.
// The returned summary is the winning choice/summary when available.
func (c *Stage) checkConsensus(ctx context.Context, st *pipeline.State, members []*member, method string, emit events.Emitter, roundsUsed int) (bool, string) {
	var reached bool
	var summary string

	switch method {
	case "similarity":
		reached = c.checkSimilarity(ctx, members)
	case "judge":
		reached, summary = c.checkJudge(ctx, st, members)
	default: // vote
		reached, summary = c.checkVote(members, emit)
	}

	emit(events.Event{
		Kind: events.KindCouncilConsensus, Stage: c.ID(),
		Payload: map[string]any{"reached": reached, "method": method, "rounds_used": roundsUsed},
	})
	return reached, summary
}

// checkVote: consensus when ≥ Threshold fraction converge on the same choice.
func (c *Stage) checkVote(members []*member, emit events.Emitter) (bool, string) {
	threshold := c.Consensus.Threshold
	if threshold == 0 {
		threshold = 0.66
	}
	counts := map[string]int{}
	voters := 0
	for _, m := range members {
		if !m.hasVote {
			continue
		}
		voters++
		key := normalizeChoice(m.vote.Choice)
		counts[key]++
		emit(events.Event{
			Kind: events.KindCouncilVote, Stage: c.ID(), Actor: m.p.Name(),
			Payload: map[string]any{
				"model": m.p.Name(), "choice": m.vote.Choice,
				"confidence": m.vote.Confidence, "agree_with_group": m.vote.AgreeWithGroup,
			},
		})
	}
	if voters == 0 {
		return false, ""
	}
	best, bestCount := "", 0
	for choice, n := range counts {
		if n > bestCount {
			best, bestCount = choice, n
		}
	}
	// Fraction is over all members: silent members can't create consensus.
	return float64(bestCount)/float64(len(members)) >= threshold, best
}

func normalizeChoice(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

// checkSimilarity: consensus when every pairwise cosine similarity of the
// members' current conclusions exceeds the threshold.
func (c *Stage) checkSimilarity(ctx context.Context, members []*member) bool {
	if c.Embedder == nil {
		return false
	}
	threshold := c.Consensus.Threshold
	if threshold == 0 {
		threshold = 0.85
	}
	texts := make([]string, len(members))
	for i, m := range members {
		texts[i] = m.position
	}
	vecs, err := c.Embedder.Embed(ctx, texts)
	if err != nil || len(vecs) != len(members) {
		return false
	}
	for i := 0; i < len(vecs); i++ {
		for j := i + 1; j < len(vecs); j++ {
			if cosine(vecs[i], vecs[j]) < threshold {
				return false
			}
		}
	}
	return true
}

func cosine(a, b []float32) float64 {
	if len(a) != len(b) || len(a) == 0 {
		return 0
	}
	var dot, na, nb float64
	for i := range a {
		dot += float64(a[i]) * float64(b[i])
		na += float64(a[i]) * float64(a[i])
		nb += float64(b[i]) * float64(b[i])
	}
	if na == 0 || nb == 0 {
		return 0
	}
	return dot / (math.Sqrt(na) * math.Sqrt(nb))
}

type judgeVerdict struct {
	Converged bool     `json:"converged"`
	Summary   string   `json:"summary"`
	Dissent   []string `json:"dissent"`
}

// checkJudge: a referee model reads all positions and rules on convergence.
func (c *Stage) checkJudge(ctx context.Context, st *pipeline.State, members []*member) (bool, string) {
	if c.Referee == nil {
		return false, ""
	}
	resp, err := c.Referee.Generate(ctx, provider.Request{
		System: `You are the referee of a council debate. Decide whether the members' positions have converged on materially the same answer.
Respond with JSON ONLY, no prose, no fences:
{"converged": bool, "summary": "one-line shared answer if converged", "dissent": ["unresolved point", ...]}`,
		Messages:  []provider.Message{provider.UserText("Positions:\n\n" + c.positionDigest(members))},
		MaxTokens: 500,
		Metadata:  map[string]string{"task_id": st.TaskID, "stage": c.ID(), "role": "judge"},
	})
	if err != nil {
		return false, ""
	}
	st.AddTurn(c.ID(), "judge", resp.Text(), resp.Usage)
	var v judgeVerdict
	if err := jsonx.Parse(resp.Text(), &v); err != nil {
		return false, ""
	}
	// Surface judge-noted holdouts even when the debate later converges or
	// deadlocks another way — genuine disagreement must not be papered over.
	if len(v.Dissent) > 0 {
		st.Meta[MetaDissent] = strings.Join(v.Dissent, "\n\n")
	}
	return v.Converged, v.Summary
}

// resolveDeadlock applies the configured OnDeadlock path when rounds are
// exhausted without consensus (spec 06 §4).
func (c *Stage) resolveDeadlock(ctx context.Context, st *pipeline.State, members []*member, emit events.Emitter, roundsUsed int) {
	mode := c.OnDeadlock
	if mode == "" {
		mode = "synthesis_notes_dissent"
	}

	switch mode {
	case "referee":
		if c.Referee != nil {
			resp, err := c.Referee.Generate(ctx, provider.Request{
				System:    "You are the referee. The council deadlocked. Pick the strongest standing position and state the final answer, briefly justifying the pick.",
				Messages:  []provider.Message{provider.UserText("Task:\n" + st.Original + "\n\nStanding positions:\n\n" + c.positionDigest(members))},
				MaxTokens: c.MaxTokens,
				Metadata:  map[string]string{"task_id": st.TaskID, "stage": c.ID(), "role": "referee"},
			})
			if err == nil {
				st.AddTurn(c.ID(), "referee", resp.Text(), resp.Usage)
				st.Meta[MetaWinner] = resp.Text()
			}
		}
	case "majority_vote":
		st.Meta[MetaWinner] = c.majorityChoice(members)
	default: // synthesis_notes_dissent
		mode = "synthesis_notes_dissent"
		st.Meta[MetaDissent] = c.positionDigest(members)
	}

	st.Meta[MetaOutcome] = mode
	emit(events.Event{
		Kind: events.KindCouncilConsensus, Stage: c.ID(),
		Payload: map[string]any{"reached": false, "method": mode, "rounds_used": roundsUsed},
	})
}

// majorityChoice takes the plurality choice; ties broken by aggregate confidence.
func (c *Stage) majorityChoice(members []*member) string {
	type tally struct {
		count int
		conf  float64
		label string
	}
	tallies := map[string]*tally{}
	for _, m := range members {
		if !m.hasVote {
			continue
		}
		key := normalizeChoice(m.vote.Choice)
		t, ok := tallies[key]
		if !ok {
			t = &tally{label: m.vote.Choice}
			tallies[key] = t
		}
		t.count++
		t.conf += m.vote.Confidence
	}
	keys := make([]string, 0, len(tallies))
	for k := range tallies {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		a, b := tallies[keys[i]], tallies[keys[j]]
		if a.count != b.count {
			return a.count > b.count
		}
		if a.conf != b.conf {
			return a.conf > b.conf
		}
		return keys[i] < keys[j]
	})
	if len(keys) == 0 {
		return ""
	}
	w := tallies[keys[0]]
	return fmt.Sprintf("%s (plurality: %d votes, aggregate confidence %.2f)", w.label, w.count, w.conf)
}
