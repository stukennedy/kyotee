package tui

import (
	"context"
	"os"

	"github.com/stukennedy/tooey/app"
	"golang.org/x/term"
)

// Run starts the TUI against an engine at baseURL, taking the terminal into
// raw mode for the duration.
func Run(ctx context.Context, baseURL string) error {
	oldState, err := term.MakeRaw(int(os.Stdin.Fd()))
	if err == nil {
		defer term.Restore(int(os.Stdin.Fd()), oldState)
	}

	client := NewClient(baseURL)
	a := &app.App{
		Init:   func() interface{} { return NewModel(client) },
		Update: Update,
		View:   View,
	}
	return a.Run(ctx)
}
