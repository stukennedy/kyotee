package pipeline

import (
	"context"

	"github.com/stukennedy/kyotee/internal/events"
)

// Output is the terminal stage of every pipeline: it promotes Draft → Final.
// The Executor emits task.final after the pipeline completes.
type Output struct{}

func (Output) ID() string { return "output" }

func (Output) Run(_ context.Context, st *State, _ events.Emitter) (*State, error) {
	if st.Final == "" {
		st.Final = st.Draft
	}
	return st, nil
}
