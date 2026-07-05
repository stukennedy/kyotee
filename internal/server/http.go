package server

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/stukennedy/kyotee/internal/receptionist"
)

// Handler builds the engine's HTTP mux:
//
//	POST /v1/tasks                {text, overrides?} → {task_id}
//	GET  /v1/tasks                → [TaskInfo]
//	GET  /v1/tasks/{id}           → persisted State
//	GET  /v1/tasks/{id}/events    → SSE, replay from Seq 0 + live + ": ping"
//	POST /v1/tasks/{id}/resume    → 202
//	GET  /v1/config               → effective config (YAML)
//	PUT  /v1/config               → hot-reload; 400 keeps old config live
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

	return mux
}

// handleSSE streams a task's events one JSON object per data: line. The bus
// replays retained history first, so connecting mid- or post-run
// reconstructs the full state; the `id:` field carries Seq for client de-dup.
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

	ch, cancel := e.Bus.Subscribe(taskID)
	defer cancel()

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
			data, err := json.Marshal(ev)
			if err != nil {
				continue
			}
			fmt.Fprintf(w, "id: %d\nevent: engine\ndata: %s\n\n", ev.Seq, data)
			flusher.Flush()
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
