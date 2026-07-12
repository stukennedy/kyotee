package tui

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/stukennedy/tooey/app"
	"github.com/stukennedy/tooey/sse"

	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/receptionist"
)

// Client performs the TUI's HTTP actions and SSE subscription against the
// engine. All results come back as messages; the TUI never mutates
// orchestration state locally.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{BaseURL: baseURL, HTTP: &http.Client{Timeout: 30 * time.Second}}
}

// SubmitCmd POSTs a task. A non-empty threadID continues that conversation;
// the response carries the (possibly newly minted) thread_id back so the TUI
// can keep threading follow-ups.
func (c *Client) SubmitCmd(text, threadID string, ov receptionist.Overrides) app.Cmd {
	return func() app.Msg {
		body, _ := json.Marshal(map[string]any{"text": text, "thread_id": threadID, "overrides": ov})
		resp, err := c.HTTP.Post(c.BaseURL+"/v1/tasks", "application/json", bytes.NewReader(body))
		if err != nil {
			return TaskCreatedMsg{Err: err}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			return TaskCreatedMsg{Err: apiError(resp)}
		}
		var out struct {
			TaskID   string `json:"task_id"`
			ThreadID string `json:"thread_id"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return TaskCreatedMsg{Err: err}
		}
		return TaskCreatedMsg{TaskID: out.TaskID, ThreadID: out.ThreadID}
	}
}

// StreamSub opens the task's SSE stream and forwards each engine event as an
// SSEMsg. Tooey's SSE client auto-reconnects (the engine replays from Seq 0
// on each connect; the model de-dups by Seq) until either the parent ctx is
// cancelled (a new task superseded this stream) or the engine sends the
// terminal "done" frame — without those two exits, every viewed task would
// leak a reconnect loop for the rest of the session.
func (c *Client) StreamSub(ctx context.Context, taskID string) app.Sub {
	return func(send func(app.Msg)) app.Msg {
		sctx, cancel := context.WithCancel(ctx)
		defer cancel() // stops the sse.Client reconnect goroutine on return
		client := &sse.Client{
			URL:        c.BaseURL + "/v1/tasks/" + taskID + "/events",
			RetryDelay: 2 * time.Second,
		}
		ch, err := client.Connect(sctx)
		if err != nil {
			send(SSEStatusMsg{Connected: false})
			return nil
		}
		send(SSEStatusMsg{Connected: true})
		for raw := range ch {
			if raw.Type == "done" {
				send(SSEStatusMsg{Connected: false})
				return nil
			}
			var ev events.Event
			if err := json.Unmarshal(raw.Data, &ev); err != nil {
				continue
			}
			send(SSEMsg{Event: ev})
		}
		send(SSEStatusMsg{Connected: false})
		return nil
	}
}

// HealthSub polls GET /v1/healthz and reports engine reachability as a stream
// of HealthMsg. Unlike the per-task SSE stream it lives for the whole session,
// so the header can distinguish "engine reachable" from "a task is streaming".
func (c *Client) HealthSub(ctx context.Context) app.Sub {
	return func(send func(app.Msg)) app.Msg {
		check := func() {
			cctx, cancel := context.WithTimeout(ctx, 2*time.Second)
			defer cancel()
			req, err := http.NewRequestWithContext(cctx, http.MethodGet, c.BaseURL+"/v1/healthz", nil)
			if err != nil {
				send(HealthMsg{Up: false})
				return
			}
			resp, err := c.HTTP.Do(req)
			up := err == nil && resp.StatusCode == http.StatusOK
			if resp != nil {
				resp.Body.Close()
			}
			send(HealthMsg{Up: up})
		}
		check() // report reachability immediately, don't wait a full interval
		ticker := time.NewTicker(3 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				check()
			}
		}
	}
}

func (c *Client) FetchConfigCmd() app.Cmd {
	return func() app.Msg {
		resp, err := c.HTTP.Get(c.BaseURL + "/v1/config")
		if err != nil {
			return ConfigFetchedMsg{Err: err}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return ConfigFetchedMsg{Err: apiError(resp)}
		}
		raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		if err != nil {
			return ConfigFetchedMsg{Err: err}
		}
		return ConfigFetchedMsg{YAML: string(raw)}
	}
}

func (c *Client) SaveConfigCmd(yamlText string) app.Cmd {
	return func() app.Msg {
		req, err := http.NewRequest(http.MethodPut, c.BaseURL+"/v1/config", bytes.NewReader([]byte(yamlText)))
		if err != nil {
			return ConfigSavedMsg{Err: err}
		}
		req.Header.Set("Content-Type", "application/yaml")
		resp, err := c.HTTP.Do(req)
		if err != nil {
			return ConfigSavedMsg{Err: err}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return ConfigSavedMsg{Err: apiError(resp)}
		}
		return ConfigSavedMsg{}
	}
}

func (c *Client) ListTasksCmd() app.Cmd {
	return func() app.Msg {
		resp, err := c.HTTP.Get(c.BaseURL + "/v1/tasks")
		if err != nil {
			return TasksListedMsg{Err: err}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return TasksListedMsg{Err: apiError(resp)}
		}
		var tasks []TaskSummary
		if err := json.NewDecoder(resp.Body).Decode(&tasks); err != nil {
			return TasksListedMsg{Err: err}
		}
		return TasksListedMsg{Tasks: tasks}
	}
}

func (c *Client) ResumeCmd(taskID string) app.Cmd {
	return func() app.Msg {
		resp, err := c.HTTP.Post(c.BaseURL+"/v1/tasks/"+taskID+"/resume", "application/json", nil)
		if err != nil {
			return ResumedMsg{Err: err}
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			return ResumedMsg{Err: apiError(resp)}
		}
		return ResumedMsg{TaskID: taskID}
	}
}

func apiError(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &body) == nil && body.Error != "" {
		return fmt.Errorf("%s (HTTP %d)", body.Error, resp.StatusCode)
	}
	return fmt.Errorf("HTTP %d", resp.StatusCode)
}
