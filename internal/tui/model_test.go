package tui

import (
	"strings"
	"testing"

	"github.com/stukennedy/tooey/app"
	"github.com/stukennedy/tooey/input"
	"github.com/stukennedy/tooey/tooeytest"
)

func runeKey(r rune) app.Msg { return app.KeyMsg{Key: input.Key{Type: input.RuneKey, Rune: r}} }
func typeKey(t input.KeyType) app.Msg { return app.KeyMsg{Key: input.Key{Type: t}} }

// The reported bug: in the default (INSERT) mode, command letters must be
// typed into the prompt, never intercepted as commands.
func TestInsertModeTypesCommandLetters(t *testing.T) {
	m := NewModel(NewClient("http://localhost:0"))
	if m.Mode != modeInsert {
		t.Fatalf("default mode should be INSERT, got %v", m.Mode)
	}
	for _, r := range "config parser" {
		Update(m, runeKey(r))
	}
	if m.Active != overlayNone {
		t.Fatalf("typing in INSERT mode opened a modal: %v", m.Active)
	}
	if m.Input.Value != "config parser" {
		t.Fatalf("expected prompt %q, got %q", "config parser", m.Input.Value)
	}
}

// In NORMAL mode the same letters are commands and must not leak into the prompt.
func TestNormalModeLettersAreCommands(t *testing.T) {
	m := NewModel(NewClient("http://localhost:0"))
	Update(m, typeKey(input.Escape)) // INSERT -> NORMAL
	if m.Mode != modeNormal {
		t.Fatal("Escape did not switch to NORMAL mode")
	}

	Update(m, runeKey('o'))
	if m.Active != overlayOverride {
		t.Fatalf("NORMAL 'o' did not open override, active=%v", m.Active)
	}

	m.Active = overlayNone
	res := Update(m, runeKey('c'))
	if !strings.Contains(m.Status, "config") {
		t.Fatalf("NORMAL 'c' did not trigger config fetch, status=%q", m.Status)
	}
	if len(res.Cmds) == 0 {
		t.Fatal("expected a config-fetch command from NORMAL 'c'")
	}
	if m.Input.Value != "" {
		t.Fatalf("command letter leaked into prompt: %q", m.Input.Value)
	}
}

func TestModeToggle(t *testing.T) {
	m := NewModel(NewClient("http://localhost:0"))
	Update(m, typeKey(input.Escape))
	if m.Mode != modeNormal {
		t.Fatal("Esc should enter NORMAL")
	}
	Update(m, runeKey('i'))
	if m.Mode != modeInsert {
		t.Fatal("i should enter INSERT")
	}
}

// The offline-indicator fix: the badge tracks engine reachability, not the
// per-task stream, and never shows a false "offline".
func TestHeaderConnectionStates(t *testing.T) {
	m := NewModel(NewClient("http://localhost:0"))

	frame := tooeytest.RenderText(m.viewHeader(), 80, 1)
	if !strings.Contains(frame, "connecting") {
		t.Fatalf("pre-poll header should show connecting:\n%s", frame)
	}

	m.pollStarted, m.EngineUp = true, false
	frame = tooeytest.RenderText(m.viewHeader(), 80, 1)
	if !strings.Contains(frame, "offline") {
		t.Fatalf("engine-down header should show offline:\n%s", frame)
	}

	m.EngineUp = true // reachable but no task streaming
	frame = tooeytest.RenderText(m.viewHeader(), 80, 1)
	if !strings.Contains(frame, "live") {
		t.Fatalf("engine-up header should show live even between tasks:\n%s", frame)
	}
}

func TestSoloAnswerRendersMarkdown(t *testing.T) {
	m := NewModel(NewClient("http://localhost:0"))
	m.Final = "# Title\n\nSome **bold** text and `code`."
	frame := tooeytest.RenderText(m.viewSolo(), 120, 12)
	for _, want := range []string{"Title", "bold", "code"} {
		if !strings.Contains(frame, want) {
			t.Fatalf("markdown answer missing %q:\n%s", want, frame)
		}
	}
}
