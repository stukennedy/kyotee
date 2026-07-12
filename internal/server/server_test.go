package server

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stukennedy/kyotee/internal/config"
	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/receptionist"
	"github.com/stukennedy/kyotee/internal/state"
)

func mockConfig() *config.Config {
	c := &config.Config{
		Version: 1,
		Providers: []config.Provider{
			{Name: "cheap", Vendor: "mock"},
			{Name: "mid", Vendor: "mock"},
		},
		Receptionist: config.Receptionist{
			Model: "cheap",
			Routes: []config.Route{
				{Strategy: "solo", Thinking: "fast", Models: config.Models{Primary: "mid"}},
			},
		},
	}
	c.ApplyDefaults()
	return c
}

func newTestEngine(t *testing.T, dir string) *Engine {
	t.Helper()
	store, err := state.NewFileStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	return NewEngine(mockConfig(), store)
}

func waitForFinal(t *testing.T, e *Engine, taskID string) {
	t.Helper()
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("task did not finish")
		default:
		}
		if st, err := e.Store.Load(taskID); err == nil && st.Final != "" {
			// Give the async event log a moment to flush the tail.
			time.Sleep(50 * time.Millisecond)
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// readSSE collects events until "event: done" or timeout.
func readSSE(t *testing.T, url string) (kinds []string, sawDone bool) {
	t.Helper()
	resp, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if ct := resp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	timeout := time.AfterFunc(3*time.Second, func() { resp.Body.Close() })
	defer timeout.Stop()

	scanner := bufio.NewScanner(resp.Body)
	current := ""
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "event: ") {
			current = strings.TrimPrefix(line, "event: ")
			if current == "done" {
				return kinds, true
			}
		}
		if strings.HasPrefix(line, "data: ") && current != "done" {
			var ev events.Event
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
				t.Fatalf("bad event JSON: %v", err)
			}
			kinds = append(kinds, ev.Kind)
		}
	}
	return kinds, false
}

func TestSubmitStreamAndDone(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	srv := httptest.NewServer(e.Handler())
	defer srv.Close()

	body, _ := json.Marshal(map[string]any{"text": "hello engine"})
	resp, err := http.Post(srv.URL+"/v1/tasks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var created struct {
		TaskID string `json:"task_id"`
	}
	json.NewDecoder(resp.Body).Decode(&created)
	waitForFinal(t, e, created.TaskID)

	// Late connect: full replay from Seq 0, terminated by event: done.
	kinds, sawDone := readSSE(t, srv.URL+"/v1/tasks/"+created.TaskID+"/events")
	if !sawDone {
		t.Fatal("stream did not send event: done")
	}
	seen := map[string]bool{}
	for _, k := range kinds {
		seen[k] = true
	}
	for _, k := range []string{events.KindTaskReceived, events.KindTaskClassified,
		events.KindTaskRouted, events.KindStageStart, events.KindStageEnd, events.KindTaskFinal} {
		if !seen[k] {
			t.Fatalf("replayed stream missing %s (saw %v)", k, kinds)
		}
	}
}

// A follow-up in a thread must carry the prior turn's Q&A into the new task
// so the solving stage answers with conversational context.
func TestConversationThreadCarriesContext(t *testing.T) {
	dir := t.TempDir()
	e := newTestEngine(t, dir)

	id1, thread, err := e.Submit("what is the capital of France?", receptionist.Overrides{}, "")
	if err != nil {
		t.Fatal(err)
	}
	if thread != id1 {
		t.Fatalf("first task should mint a thread named after itself: thread=%q id=%q", thread, id1)
	}
	waitForFinal(t, e, id1)

	id2, thread2, err := e.Submit("and its population?", receptionist.Overrides{}, thread)
	if err != nil {
		t.Fatal(err)
	}
	if thread2 != thread {
		t.Fatalf("follow-up should stay in the same thread: %q != %q", thread2, thread)
	}
	waitForFinal(t, e, id2)

	st2, err := e.Store.Load(id2)
	if err != nil {
		t.Fatal(err)
	}
	if st2.ParentID != id1 {
		t.Fatalf("turn 2 parent = %q, want %q", st2.ParentID, id1)
	}
	if len(st2.History) != 1 {
		t.Fatalf("turn 2 should carry exactly 1 prior exchange, got %d", len(st2.History))
	}
	if st2.History[0].User != "what is the capital of France?" || st2.History[0].Assistant == "" {
		t.Fatalf("turn 2 history did not capture turn 1 Q&A: %+v", st2.History[0])
	}
	if body := st2.PromptBody(); !strings.Contains(body, "capital of France") || !strings.Contains(body, "Current request:") {
		t.Fatalf("turn 2 solving prompt missing conversation context:\n%s", body)
	}
}

// Replay must survive an engine restart via the persisted event log.
func TestReplayAcrossEngineRestart(t *testing.T) {
	dir := t.TempDir()
	e1 := newTestEngine(t, dir)
	taskID, _, err := e1.Submit("persist me", receptionist.Overrides{}, "")
	if err != nil {
		t.Fatal(err)
	}
	waitForFinal(t, e1, taskID)

	// New engine, same state dir — in-memory bus is empty.
	e2 := newTestEngine(t, dir)
	srv := httptest.NewServer(e2.Handler())
	defer srv.Close()

	kinds, sawDone := readSSE(t, srv.URL+"/v1/tasks/"+taskID+"/events")
	if !sawDone {
		t.Fatal("post-restart stream did not terminate with done")
	}
	if len(kinds) == 0 || kinds[len(kinds)-1] != events.KindTaskFinal {
		t.Fatalf("post-restart replay incomplete: %v", kinds)
	}
}

func TestInvalidOverrideReturns400(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	srv := httptest.NewServer(e.Handler())
	defer srv.Close()

	body := `{"text": "task", "overrides": {"strategy": "galactic_senate"}}`
	resp, err := http.Post(srv.URL+"/v1/tasks", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("invalid override: got %d, want 400", resp.StatusCode)
	}
	// Task must not have started.
	tasks, _ := e.Tasks()
	if len(tasks) != 0 {
		t.Fatalf("task started despite invalid override: %+v", tasks)
	}
}

func TestConfigHotReloadAndRejection(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	srv := httptest.NewServer(e.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/config")
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 1<<16)
	n, _ := resp.Body.Read(raw)
	resp.Body.Close()
	if !strings.Contains(string(raw[:n]), "cheap") {
		t.Fatal("config GET missing provider names")
	}

	put := func(body string) int {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/v1/config", strings.NewReader(body))
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		return r.StatusCode
	}
	if code := put("version: 1\nreceptionist:\n  routes:\n    - strategy: bogus\n"); code != http.StatusBadRequest {
		t.Fatalf("invalid config accepted: %d", code)
	}
	if e.Holder.Get().Receptionist.Model != "cheap" {
		t.Fatal("old config was clobbered by invalid PUT")
	}

	valid := `
version: 1
providers:
  - {name: newmodel, vendor: mock}
receptionist:
  model: newmodel
  routes:
    - {strategy: solo, thinking: fast, models: {primary: newmodel}}
`
	if code := put(valid); code != http.StatusOK {
		t.Fatalf("valid config rejected: %d", code)
	}
	if e.Holder.Get().Receptionist.Model != "newmodel" {
		t.Fatal("config did not hot-reload")
	}
}

func TestProvidersAndHealthz(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	srv := httptest.NewServer(e.Handler())
	defer srv.Close()

	resp, err := http.Get(srv.URL + "/v1/providers")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var provs []ProviderInfo
	if err := json.NewDecoder(resp.Body).Decode(&provs); err != nil {
		t.Fatal(err)
	}
	if len(provs) != 2 {
		t.Fatalf("want 2 providers, got %+v", provs)
	}

	hz, err := http.Get(srv.URL + "/v1/healthz")
	if err != nil {
		t.Fatal(err)
	}
	hz.Body.Close()
	if hz.StatusCode != http.StatusOK {
		t.Fatalf("healthz %d", hz.StatusCode)
	}
}

func TestResumeEndpoint(t *testing.T) {
	e := newTestEngine(t, t.TempDir())
	srv := httptest.NewServer(e.Handler())
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/v1/tasks/nope/resume", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("resume of unknown task: %d", resp.StatusCode)
	}
}
