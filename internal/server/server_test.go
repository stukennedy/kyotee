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
	"github.com/stukennedy/kyotee/internal/state"
)

func mockConfig() *config.Config {
	c := &config.Config{
		Providers: []config.Provider{
			{Name: "cheap", Kind: "mock", Vendor: "anthropic"},
			{Name: "mid", Kind: "mock", Vendor: "anthropic"},
		},
		Models: config.ModelRoles{Receptionist: "cheap", Default: "mid"},
		Routes: []config.Route{
			{Strategy: "solo", Thinking: "fast", Models: config.Models{Primary: "mid"}},
		},
	}
	c.Defaults()
	return c
}

func newTestEngine(t *testing.T) *Engine {
	t.Helper()
	store, err := state.NewFileStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return NewEngine(mockConfig(), store)
}

func TestSubmitAndStreamToCompletion(t *testing.T) {
	e := newTestEngine(t)
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
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		t.Fatal(err)
	}

	// Wait for the pipeline to finish, then connect the SSE stream late —
	// replay from Seq 0 must reconstruct the full run.
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("task did not finish")
		default:
		}
		if st, err := e.Store.Load(created.TaskID); err == nil && st.Final != "" {
			goto stream
		}
		time.Sleep(20 * time.Millisecond)
	}

stream:
	sseResp, err := http.Get(srv.URL + "/v1/tasks/" + created.TaskID + "/events")
	if err != nil {
		t.Fatal(err)
	}
	defer sseResp.Body.Close()
	if ct := sseResp.Header.Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}

	seen := map[string]bool{}
	scanner := bufio.NewScanner(sseResp.Body)
	timeout := time.AfterFunc(3*time.Second, func() { sseResp.Body.Close() })
	defer timeout.Stop()
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev events.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("bad event JSON: %v", err)
		}
		seen[ev.Kind] = true
		if ev.Kind == events.KindTaskFinal {
			break
		}
	}
	for _, k := range []string{events.KindTaskReceived, events.KindTaskClassified,
		events.KindTaskRouted, events.KindStageStart, events.KindStageEnd, events.KindTaskFinal} {
		if !seen[k] {
			t.Fatalf("replayed stream missing %s (saw %v)", k, seen)
		}
	}
}

func TestConfigHotReloadAndRejection(t *testing.T) {
	e := newTestEngine(t)
	srv := httptest.NewServer(e.Handler())
	defer srv.Close()

	// GET returns YAML including our provider.
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

	// Invalid PUT → 400, old config stays live.
	put := func(body string) int {
		req, _ := http.NewRequest(http.MethodPut, srv.URL+"/v1/config", strings.NewReader(body))
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer r.Body.Close()
		return r.StatusCode
	}
	if code := put("routes:\n  - strategy: bogus\n"); code != http.StatusBadRequest {
		t.Fatalf("invalid config accepted: %d", code)
	}
	if e.Holder.Get().Models.Receptionist != "cheap" {
		t.Fatal("old config was clobbered by invalid PUT")
	}

	// Valid PUT hot-swaps.
	valid := `
providers:
  - {name: newmodel, kind: mock}
models: {receptionist: newmodel, default: newmodel}
routes:
  - {strategy: solo, thinking: fast, models: {primary: newmodel}}
`
	if code := put(valid); code != http.StatusOK {
		t.Fatalf("valid config rejected: %d", code)
	}
	if e.Holder.Get().Models.Receptionist != "newmodel" {
		t.Fatal("config did not hot-reload")
	}
}

func TestResumeEndpoint(t *testing.T) {
	e := newTestEngine(t)
	srv := httptest.NewServer(e.Handler())
	defer srv.Close()

	// Unknown task → 400.
	resp, err := http.Post(srv.URL+"/v1/tasks/nope/resume", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("resume of unknown task: %d", resp.StatusCode)
	}
}
