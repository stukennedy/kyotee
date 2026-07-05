package council

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/pipeline"
	"github.com/stukennedy/kyotee/internal/provider"
)

func collect() (events.Emitter, *[]events.Event, *sync.Mutex) {
	var mu sync.Mutex
	var evs []events.Event
	return func(ev events.Event) {
		mu.Lock()
		defer mu.Unlock()
		evs = append(evs, ev)
	}, &evs, &mu
}

func kinds(evs []events.Event, kind string) []events.Event {
	var out []events.Event
	for _, ev := range evs {
		if ev.Kind == kind {
			out = append(out, ev)
		}
	}
	return out
}

// scriptedMember diverges in its opening then converges on choice "A".
func scriptedMember(name, vendor, opening, converged string) *provider.Fake {
	return &provider.Fake{ModelName: name, VendorName: vendor,
		ScriptFn: func(call int, req provider.Request) (provider.Response, error) {
			if call == 0 {
				return provider.TextResponse(opening, 50, 50), nil
			}
			return provider.TextResponse(converged, 50, 50), nil
		}}
}

func TestVoteConsensusAndSynthesis(t *testing.T) {
	voteA := `I now agree with the group. {"choice": "A", "confidence": 0.9, "agree_with_group": true}`
	members := []provider.Provider{
		scriptedMember("m1", "anthropic", `Position Alpha. {"choice": "A", "confidence": 0.8, "agree_with_group": false}`, voteA),
		scriptedMember("m2", "openai", `Position Beta. {"choice": "B", "confidence": 0.7, "agree_with_group": false}`, voteA),
		scriptedMember("m3", "google", `Position Gamma. {"choice": "C", "confidence": 0.6, "agree_with_group": false}`, voteA),
	}
	st := pipeline.NewState("c1", "hard question")
	emit, evs, mu := collect()

	stage := &Stage{Members: members, Rounds: 3, Consensus: ConsensusConfig{Method: "vote", Threshold: 0.66}}
	if _, err := stage.Run(context.Background(), st, emit); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()

	if st.Meta[MetaOutcome] != "consensus" {
		t.Fatalf("expected consensus, got %q", st.Meta[MetaOutcome])
	}
	if n := len(kinds(*evs, events.KindCouncilOpening)); n != 3 {
		t.Fatalf("want 3 openings, got %d", n)
	}
	if len(kinds(*evs, events.KindCouncilVote)) == 0 {
		t.Fatal("expected council.vote events")
	}
	cons := kinds(*evs, events.KindCouncilConsensus)
	if len(cons) == 0 || cons[len(cons)-1].Payload["reached"] != true {
		t.Fatalf("expected final consensus reached=true, got %+v", cons)
	}

	// Synthesis collapses the debate into the Draft.
	synth := provider.NewFake("synth", "anthropic", provider.TextResponse("Final synthesised answer: A.", 100, 50))
	sy := &Synthesis{Model: synth}
	if _, err := sy.Run(context.Background(), st, emit); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(st.Draft, "synthesised") {
		t.Fatalf("draft not from synthesis: %q", st.Draft)
	}
	// Synthesis prompt must include the full debate transcript.
	req := synth.Requests[0]
	if !strings.Contains(req.Messages[0].Content[0].Text, "Position Beta") {
		t.Fatal("synthesis prompt missing debate transcript")
	}
}

type fakeEmbedder struct{ vecs map[string][]float32 }

func (f *fakeEmbedder) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i, tx := range texts {
		v, ok := f.vecs[tx]
		if !ok {
			v = []float32{1, 0}
		}
		out[i] = v
	}
	return out, nil
}

func TestSimilarityConsensus(t *testing.T) {
	near := &fakeEmbedder{vecs: map[string][]float32{
		"same answer one": {1, 0.01}, "same answer two": {1, 0.02}, "same answer three": {1, 0.03},
	}}
	far := &fakeEmbedder{vecs: map[string][]float32{
		"same answer one": {1, 0}, "same answer two": {0, 1}, "same answer three": {1, 1},
	}}

	mk := func(texts [3]string) []*member {
		return []*member{
			{p: provider.NewFake("m1", "a"), position: texts[0]},
			{p: provider.NewFake("m2", "b"), position: texts[1]},
			{p: provider.NewFake("m3", "c"), position: texts[2]},
		}
	}
	texts := [3]string{"same answer one", "same answer two", "same answer three"}

	stage := &Stage{Consensus: ConsensusConfig{Method: "similarity", Threshold: 0.85}, Embedder: near}
	if !stage.checkSimilarity(context.Background(), mk(texts)) {
		t.Fatal("near-identical conclusions should reach similarity consensus")
	}
	stage.Embedder = far
	if stage.checkSimilarity(context.Background(), mk(texts)) {
		t.Fatal("divergent conclusions must not reach similarity consensus")
	}
}

func TestJudgeConsensus(t *testing.T) {
	st := pipeline.NewState("cj", "q")
	members := []*member{
		{p: provider.NewFake("m1", "a"), position: "pos1"},
		{p: provider.NewFake("m2", "b"), position: "pos2"},
	}
	judgeYes := provider.NewFake("judge", "anthropic",
		provider.TextResponse(`{"converged": true, "summary": "both say X", "dissent": []}`, 20, 20))
	stage := &Stage{Referee: judgeYes, Consensus: ConsensusConfig{Method: "judge"}}
	reached, summary := stage.checkJudge(context.Background(), st, members)
	if !reached || summary != "both say X" {
		t.Fatalf("judge should report converged, got %v %q", reached, summary)
	}

	judgeNo := provider.NewFake("judge", "anthropic",
		provider.TextResponse(`{"converged": false, "summary": "", "dissent": ["timeline"]}`, 20, 20))
	stage.Referee = judgeNo
	if reached, _ := stage.checkJudge(context.Background(), st, members); reached {
		t.Fatal("judge should report not converged")
	}
}

func TestVendorDiversityWarning(t *testing.T) {
	members := []provider.Provider{
		scriptedMember("m1", "anthropic", `x {"choice": "A", "confidence": 1, "agree_with_group": true}`, ""),
		scriptedMember("m2", "anthropic", `y {"choice": "A", "confidence": 1, "agree_with_group": true}`, ""),
	}
	st := pipeline.NewState("cw", "q")
	emit, evs, mu := collect()
	stage := &Stage{Members: members, Rounds: 1}
	if _, err := stage.Run(context.Background(), st, emit); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	warned := false
	for _, ev := range kinds(*evs, events.KindError) {
		if msg, _ := ev.Payload["message"].(string); strings.Contains(msg, "vendor") {
			warned = true
		}
	}
	if !warned {
		t.Fatal("expected same-vendor warning")
	}
}

func TestDeadlockSynthesisNotesDissent(t *testing.T) {
	stubborn := func(name, vendor, choice string) *provider.Fake {
		return &provider.Fake{ModelName: name, VendorName: vendor,
			ScriptFn: func(int, provider.Request) (provider.Response, error) {
				return provider.TextResponse(`I hold my position `+name+`. {"choice": "`+choice+`", "confidence": 0.9, "agree_with_group": false}`, 30, 30), nil
			}}
	}
	members := []provider.Provider{
		stubborn("m1", "anthropic", "A"), stubborn("m2", "openai", "B"), stubborn("m3", "google", "C"),
	}
	st := pipeline.NewState("cd", "contested question")
	emit, evs, mu := collect()
	stage := &Stage{Members: members, Rounds: 2, OnDeadlock: "synthesis_notes_dissent",
		Consensus: ConsensusConfig{Method: "vote", Threshold: 0.66}}
	if _, err := stage.Run(context.Background(), st, emit); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	if st.Meta[MetaOutcome] != "synthesis_notes_dissent" {
		t.Fatalf("expected deadlock outcome, got %q", st.Meta[MetaOutcome])
	}
	if st.Meta[MetaDissent] == "" {
		t.Fatal("dissent digest must be recorded for synthesis")
	}
	cons := kinds(*evs, events.KindCouncilConsensus)
	last := cons[len(cons)-1]
	if last.Payload["reached"] != false || last.Payload["method"] != "synthesis_notes_dissent" {
		t.Fatalf("final consensus event should record deadlock resolution, got %+v", last.Payload)
	}
	mu.Unlock()

	// Synthesis must surface the disagreement.
	var sysPrompt string
	synth := &provider.Fake{ModelName: "synth", VendorName: "anthropic",
		ScriptFn: func(_ int, req provider.Request) (provider.Response, error) {
			sysPrompt = req.System
			return provider.TextResponse("Majority view... Dissent: members disagreed A/B/C.", 100, 50), nil
		}}
	sy := &Synthesis{Model: synth}
	if _, err := sy.Run(context.Background(), st, emit); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(sysPrompt, "dissent") && !strings.Contains(sysPrompt, "Dissent") {
		t.Fatal("synthesis system prompt must demand dissent be surfaced")
	}
}

func TestDeadlockMajorityVote(t *testing.T) {
	mk := func(name, vendor, choice string, conf float64) *provider.Fake {
		script := `pos ` + name + `. {"choice": "` + choice + `", "confidence": ` +
			strconv.FormatFloat(conf, 'f', 2, 64) + `, "agree_with_group": false}`
		return &provider.Fake{ModelName: name, VendorName: vendor,
			ScriptFn: func(int, provider.Request) (provider.Response, error) {
				return provider.TextResponse(script, 30, 30), nil
			}}
	}
	members := []provider.Provider{
		mk("m1", "anthropic", "A", 0.9), mk("m2", "openai", "A", 0.8), mk("m3", "google", "B", 0.95),
	}
	st := pipeline.NewState("cm", "q")
	emit, _, _ := collect()
	stage := &Stage{Members: members, Rounds: 1, OnDeadlock: "majority_vote",
		Consensus: ConsensusConfig{Method: "vote", Threshold: 0.9}} // unreachable threshold
	if _, err := stage.Run(context.Background(), st, emit); err != nil {
		t.Fatal(err)
	}
	if st.Meta[MetaOutcome] != "majority_vote" {
		t.Fatalf("expected majority_vote outcome, got %q", st.Meta[MetaOutcome])
	}
	if !strings.Contains(st.Meta[MetaWinner], "A") {
		t.Fatalf("plurality winner should be A: %q", st.Meta[MetaWinner])
	}
}
