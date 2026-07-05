package pipeline_test

import (
	"context"
	"errors"
	"testing"

	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/pipeline"
	"github.com/stukennedy/kyotee/internal/provider"
	"github.com/stukennedy/kyotee/internal/state"
)

type stubStage struct {
	id  string
	fn  func(st *pipeline.State, emit events.Emitter) error
	ran int
}

func (s *stubStage) ID() string { return s.id }
func (s *stubStage) Run(_ context.Context, st *pipeline.State, emit events.Emitter) (*pipeline.State, error) {
	s.ran++
	if s.fn != nil {
		if err := s.fn(st, emit); err != nil {
			return st, err
		}
	}
	return st, nil
}

func newExecutor(t *testing.T) (*pipeline.Executor, *events.MemBus) {
	t.Helper()
	store, err := state.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	bus := events.NewBus()
	return &pipeline.Executor{Store: store, Bus: bus}, bus
}

func TestNoopStageEmitsLifecycleAndCheckpoints(t *testing.T) {
	ex, bus := newExecutor(t)
	st := pipeline.NewState("t1", "hello")
	noop := &stubStage{id: "noop"}

	got, err := ex.Execute(context.Background(), []pipeline.Stage{noop}, st)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Checkpointed("noop") {
		t.Fatal("expected checkpoint for noop stage")
	}

	kinds := map[string]bool{}
	for _, ev := range bus.History("t1") {
		kinds[ev.Kind] = true
	}
	for _, k := range []string{events.KindStageStart, events.KindStageEnd, events.KindTaskFinal} {
		if !kinds[k] {
			t.Fatalf("missing event kind %s (got %v)", k, kinds)
		}
	}

	// Checkpoint must be on disk and loadable.
	loaded, err := ex.Store.Load("t1")
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Checkpointed("noop") {
		t.Fatal("persisted state missing checkpoint")
	}
}

func TestBudgetHaltPromotesDraftToFinal(t *testing.T) {
	ex, bus := newExecutor(t)
	st := pipeline.NewState("t2", "spendy")
	st.Budget.LimitUSD = 1.00

	spender := &stubStage{id: "spender", fn: func(s *pipeline.State, _ events.Emitter) error {
		s.Draft = "best effort so far"
		s.Budget.Account(provider.Usage{CostUSD: 2.00, InputTokens: 100, OutputTokens: 100})
		return nil
	}}
	never := &stubStage{id: "never"}

	got, err := ex.Execute(context.Background(), []pipeline.Stage{spender, never}, st)
	if !errors.Is(err, pipeline.ErrBudgetExhausted) {
		t.Fatalf("want ErrBudgetExhausted, got %v", err)
	}
	if never.ran != 0 {
		t.Fatal("stage after budget exhaustion must not run")
	}
	if got.Final != "best effort so far" {
		t.Fatalf("Draft not promoted to Final: %q", got.Final)
	}

	var finalEv *events.Event
	for _, ev := range bus.History("t2") {
		if ev.Kind == events.KindTaskFinal {
			e := ev
			finalEv = &e
		}
	}
	if finalEv == nil || finalEv.Payload["reason"] != "budget_exhausted" {
		t.Fatalf("expected task.final with budget_exhausted reason, got %+v", finalEv)
	}
}

func TestResumeSkipsCheckpointedStages(t *testing.T) {
	ex, _ := newExecutor(t)
	st := pipeline.NewState("t3", "resume me")
	a := &stubStage{id: "a"}
	fail := &stubStage{id: "b", fn: func(*pipeline.State, events.Emitter) error {
		return errors.New("boom")
	}}

	if _, err := ex.Execute(context.Background(), []pipeline.Stage{a, fail}, st); err == nil {
		t.Fatal("expected stage error")
	}

	// Simulate restart: load persisted state, rebuild pipeline, re-run.
	loaded, err := ex.Store.Load("t3")
	if err != nil {
		t.Fatal(err)
	}
	a2 := &stubStage{id: "a"}
	b2 := &stubStage{id: "b"}
	if _, err := ex.Execute(context.Background(), []pipeline.Stage{a2, b2}, loaded); err != nil {
		t.Fatal(err)
	}
	if a2.ran != 0 {
		t.Fatal("checkpointed stage 'a' must be skipped on resume")
	}
	if b2.ran != 1 {
		t.Fatal("failed stage 'b' must re-run on resume")
	}
}

func TestEventFanOutToTwoSubscribers(t *testing.T) {
	bus := events.NewBus()
	ch1, cancel1 := bus.Subscribe("tx")
	defer cancel1()

	bus.Publish(events.Event{TaskID: "tx", Kind: "one"})
	bus.Publish(events.Event{TaskID: "other", Kind: "noise"})

	// Late subscriber must get history replay with sequence numbers intact.
	ch2, cancel2 := bus.Subscribe("tx")
	defer cancel2()
	bus.Publish(events.Event{TaskID: "tx", Kind: "two"})

	for name, ch := range map[string]<-chan events.Event{"live": ch1, "late": ch2} {
		var got []events.Event
		for len(got) < 2 {
			got = append(got, <-ch)
		}
		if got[0].Kind != "one" || got[1].Kind != "two" {
			t.Fatalf("%s subscriber: wrong events %+v", name, got)
		}
		if got[0].Seq != 0 || got[1].Seq != 1 {
			t.Fatalf("%s subscriber: wrong seqs %+v", name, got)
		}
	}
}
