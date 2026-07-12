package pipeline

import (
	"strings"
	"testing"
)

func TestPromptBodyNoHistoryIsOriginal(t *testing.T) {
	st := &State{Original: "hello"}
	if got := st.PromptBody(); got != "hello" {
		t.Fatalf("no-history PromptBody must equal Original verbatim, got %q", got)
	}
}

func TestPromptBodyWithHistoryCarriesTurns(t *testing.T) {
	st := &State{
		Original: "and the population?",
		History: []Exchange{
			{User: "capital of France?", Assistant: "Paris."},
		},
	}
	body := st.PromptBody()
	for _, want := range []string{"capital of France?", "Paris.", "Current request:", "and the population?"} {
		if !strings.Contains(body, want) {
			t.Fatalf("PromptBody missing %q:\n%s", want, body)
		}
	}
}

func TestPromptBodyTruncatesLongAnswers(t *testing.T) {
	long := strings.Repeat("x", maxHistoryAnswerRunes+500)
	st := &State{Original: "continue", History: []Exchange{{User: "q", Assistant: long}}}
	body := st.PromptBody()
	if c := strings.Count(body, "x"); c > maxHistoryAnswerRunes {
		t.Fatalf("prior answer was not truncated: %d x's > %d cap", c, maxHistoryAnswerRunes)
	}
	if !strings.Contains(body, "…") {
		t.Fatal("truncated answer should carry an ellipsis marker")
	}
}
