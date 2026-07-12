package main

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stukennedy/kyotee/internal/config"
	"github.com/stukennedy/kyotee/internal/receptionist"
	"github.com/stukennedy/kyotee/internal/server"
	"github.com/stukennedy/kyotee/internal/state"
)

func mockEngineServer(t *testing.T) (*server.Engine, *httptest.Server) {
	t.Helper()
	c := &config.Config{
		Version: 1,
		Providers: []config.Provider{
			{Name: "cheap", Vendor: "mock"},
			{Name: "a", Vendor: "mock"},
			{Name: "b", Vendor: "mock"},
			{Name: "c", Vendor: "mock"},
		},
		Receptionist: config.Receptionist{
			Model: "cheap",
			Routes: []config.Route{
				{Strategy: "solo", Thinking: "fast", Models: config.Models{Primary: "a"}},
			},
		},
		Council: config.Council{Members: []string{"a", "b", "c"}},
	}
	c.ApplyDefaults()
	store, err := state.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	eng := server.NewEngine(c, store)
	srv := httptest.NewServer(eng.Handler())
	t.Cleanup(srv.Close)
	return eng, srv
}

// ask --wait: progress to stderr, answer alone on stdout, exit nil.
func TestRemoteAskWait(t *testing.T) {
	_, srv := mockEngineServer(t)
	var stdout, stderr bytes.Buffer

	err := runRemoteAsk(srv.URL, "hello", "", receptionist.Overrides{}, true, false, &stdout, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(stdout.String()) != "(fake response)" {
		t.Fatalf("stdout should carry only the answer: %q", stdout.String())
	}
	for _, want := range []string{"· classified", "· routed", "· stage"} {
		if !strings.Contains(stderr.String(), want) {
			t.Fatalf("stderr progress missing %q:\n%s", want, stderr.String())
		}
	}
}

// Without --wait: task_id printed, returns immediately.
func TestRemoteAskNoWaitPrintsTaskID(t *testing.T) {
	eng, srv := mockEngineServer(t)
	var stdout, stderr bytes.Buffer

	if err := runRemoteAsk(srv.URL, "fire and forget", "", receptionist.Overrides{}, false, false, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	taskID := strings.TrimSpace(stdout.String())
	if taskID == "" || strings.Contains(taskID, " ") {
		t.Fatalf("expected bare task_id on stdout, got %q", stdout.String())
	}
	// The task actually runs in the background.
	deadline := time.After(5 * time.Second)
	for {
		if st, err := eng.Store.Load(taskID); err == nil && st.Final != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("background task never finished")
		case <-time.After(20 * time.Millisecond):
		}
	}
}

// --json emits the stable contract with consensus for a council run, and the
// flags build a real overrides object (strategy + rounds visible via status).
func TestRemoteAskJSONCouncil(t *testing.T) {
	_, srv := mockEngineServer(t)
	var stdout, stderr bytes.Buffer

	ov := receptionist.Overrides{Strategy: "council", CouncilRounds: 2, BudgetUSD: 50}
	if err := runRemoteAsk(srv.URL, "pick a database", "", ov, true, true, &stdout, &stderr); err != nil {
		t.Fatal(err)
	}
	var res askResult
	if err := json.Unmarshal(stdout.Bytes(), &res); err != nil {
		t.Fatalf("stdout is not the JSON contract: %v\n%s", err, stdout.String())
	}
	if res.TaskID == "" || res.Strategy != "council" || res.Answer == "" {
		t.Fatalf("incomplete JSON result: %+v", res)
	}
	if res.Consensus == nil {
		t.Fatal("council run must report consensus in JSON output")
	}
	// Mock members never converge → deadlock notes dissent.
	if res.Consensus.Reached {
		t.Fatalf("mock council should not reach consensus: %+v", res.Consensus)
	}
	if len(res.Dissent) == 0 {
		t.Fatal("synthesis_notes_dissent deadlock must surface dissent in JSON")
	}

	// status shows the persisted snapshot for the same task.
	var statusOut bytes.Buffer
	if err := runRemoteStatus(srv.URL, res.TaskID, &statusOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(statusOut.String(), `"council"`) {
		t.Fatalf("status snapshot missing strategy: %s", statusOut.String())
	}
}

// Invalid override → engine 400 → non-nil error (non-zero exit).
func TestRemoteAskEngineErrorNonZero(t *testing.T) {
	_, srv := mockEngineServer(t)
	var stdout, stderr bytes.Buffer
	err := runRemoteAsk(srv.URL, "x", "", receptionist.Overrides{Strategy: "galactic"}, true, false, &stdout, &stderr)
	if err == nil {
		t.Fatal("engine 400 must surface as an error")
	}
}

// No running engine: fail fast with actionable guidance.
func TestRemoteAskNoEngineFailsFast(t *testing.T) {
	var stdout, stderr bytes.Buffer
	start := time.Now()
	err := runRemoteAsk("http://127.0.0.1:1", "x", "", receptionist.Overrides{}, true, false, &stdout, &stderr)
	if err == nil {
		t.Fatal("expected connection failure")
	}
	if !strings.Contains(err.Error(), "is it running") || !strings.Contains(err.Error(), "KYOTEE_URL") {
		t.Fatalf("error lacks guidance: %v", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("did not fail fast")
	}
}

func TestRemoteProviders(t *testing.T) {
	_, srv := mockEngineServer(t)
	var stdout bytes.Buffer
	if err := runRemoteProviders(srv.URL, &stdout); err != nil {
		t.Fatal(err)
	}
	out := stdout.String()
	if !strings.Contains(out, "cheap") || !strings.Contains(out, "VENDOR") {
		t.Fatalf("providers table incomplete:\n%s", out)
	}
}

func TestEngineURLResolution(t *testing.T) {
	t.Setenv("KYOTEE_URL", "")
	t.Setenv("HARNESS_URL", "")
	if got := engineURL(""); got != defaultEngineURL {
		t.Fatalf("default: %q", got)
	}
	t.Setenv("HARNESS_URL", "http://h:1")
	if got := engineURL(""); got != "http://h:1" {
		t.Fatalf("HARNESS_URL: %q", got)
	}
	t.Setenv("KYOTEE_URL", "http://k:2")
	if got := engineURL(""); got != "http://k:2" {
		t.Fatalf("KYOTEE_URL wins: %q", got)
	}
	if got := engineURL("http://flag:3"); got != "http://flag:3" {
		t.Fatalf("flag wins: %q", got)
	}
}
