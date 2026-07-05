package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/receptionist"
)

// Handler builds the engine's HTTP mux (spec 02 §3):
//
//	POST /v1/tasks                {text, overrides?} → 201 {task_id}; invalid override → 400
//	GET  /v1/tasks                → [TaskInfo]
//	GET  /v1/tasks/{id}           → persisted State snapshot
//	GET  /v1/tasks/{id}/events    → SSE: replay from Seq 0, live tail, ": ping", "event: done"
//	POST /v1/tasks/{id}/resume    → 202
//	GET  /v1/config               → effective config (YAML; secrets are env names only)
//	PUT  /v1/config               → hot-reload; 400 keeps old config live
//	POST /v1/config/reload        → re-read config file from disk
//	GET  /v1/providers            → registered models + capabilities + cost
//	GET  /v1/healthz
func (e *Engine) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Text      string                 `json:"text"`
			Overrides receptionist.Overrides `json:"overrides"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
		taskID, err := e.Submit(body.Text, body.Overrides)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusCreated, map[string]string{"task_id": taskID})
	})

	mux.HandleFunc("GET /v1/tasks", func(w http.ResponseWriter, r *http.Request) {
		tasks, err := e.Tasks()
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, tasks)
	})

	mux.HandleFunc("GET /v1/tasks/{id}", func(w http.ResponseWriter, r *http.Request) {
		st, err := e.Store.Load(r.PathValue("id"))
		if err != nil {
			httpErr(w, http.StatusNotFound, "unknown task")
			return
		}
		writeJSON(w, http.StatusOK, st)
	})

	mux.HandleFunc("POST /v1/tasks/{id}/resume", func(w http.ResponseWriter, r *http.Request) {
		if err := e.Resume(r.PathValue("id")); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "resuming"})
	})

	mux.HandleFunc("GET /v1/tasks/{id}/events", e.handleSSE)

	mux.HandleFunc("GET /v1/config", func(w http.ResponseWriter, r *http.Request) {
		data, err := yaml.Marshal(e.Holder.Get())
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/yaml")
		w.Write(data)
	})

	mux.HandleFunc("PUT /v1/config", func(w http.ResponseWriter, r *http.Request) {
		raw, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if err := e.ReloadConfig(raw); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
	})

	mux.HandleFunc("POST /v1/config/reload", func(w http.ResponseWriter, r *http.Request) {
		if err := e.ReloadConfigFromDisk(); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "reloaded"})
	})

	mux.HandleFunc("GET /v1/providers", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, e.Providers())
	})

	mux.HandleFunc("GET /v1/healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	return mux
}

// terminalEvent reports whether an event ends the run (task.final, or an
// error flagged terminal). Drives the SSE "event: done" frame.
func terminalEvent(ev events.Event) bool {
	if ev.Kind == events.KindTaskFinal {
		return true
	}
	if ev.Kind == events.KindError {
		t, _ := ev.Payload["terminal"].(bool)
		return t
	}
	return false
}

// handleSSE streams a task's events one JSON object per data: line, in Seq
// order: persisted-log replay (survives engine restarts) deduplicated
// against the live bus subscription, then live tail. Sends "event: done"
// when the task reaches task.final or a terminal error, and ": ping"
// heartbeats every 15s. The id: field carries Seq for client de-dup.
func (e *Engine) handleSSE(w http.ResponseWriter, r *http.Request) {
	taskID := r.PathValue("id")
	flusher, ok := w.(http.Flusher)
	if !ok {
		httpErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()

	// Subscribe first (replays in-memory history), then overlay the
	// persisted log for anything the bus no longer has (engine restart).
	ch, cancel := e.Bus.Subscribe(taskID)
	defer cancel()

	sent := make(map[int64]bool)
	writeEvent := func(ev events.Event) bool {
		if sent[ev.Seq] {
			return false
		}
		sent[ev.Seq] = true
		data, err := json.Marshal(ev)
		if err != nil {
			return false
		}
		fmt.Fprintf(w, "id: %d\nevent: engine\ndata: %s\n\n", ev.Seq, data)
		return terminalEvent(ev)
	}
	done := func() {
		fmt.Fprint(w, "event: done\ndata: {}\n\n")
		flusher.Flush()
	}

	finished := false
	for _, ev := range e.elog.read(taskID) {
		if writeEvent(ev) {
			finished = true
		}
	}
	flusher.Flush()
	// A finished task with no new run coming: replay is complete, close.
	if finished && !e.Running(taskID) {
		done()
		return
	}

	ping := time.NewTicker(15 * time.Second)
	defer ping.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ping.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case ev, open := <-ch:
			if !open {
				return
			}
			isTerminal := writeEvent(ev)
			flusher.Flush()
			if isTerminal {
				done()
				return
			}
		}
	}
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
