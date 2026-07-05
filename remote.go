package main

// remote.go is the CLI shim from spec 09: a stateless HTTP client for a
// running kyotee engine. It is what the Claude Code Skill (skill/SKILL.md)
// shells out to. All orchestration stays in the engine.

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/pipeline"
	"github.com/stukennedy/kyotee/internal/receptionist"
)

const defaultEngineURL = "http://127.0.0.1:8484"

// engineURL resolves the engine base URL: flag > KYOTEE_URL > HARNESS_URL >
// default.
func engineURL(flag string) string {
	if flag != "" {
		return flag
	}
	if v := os.Getenv("KYOTEE_URL"); v != "" {
		return v
	}
	if v := os.Getenv("HARNESS_URL"); v != "" {
		return v
	}
	return defaultEngineURL
}

// errNoEngine wraps connection failures with the fail-fast guidance the
// spec requires.
func errNoEngine(baseURL string, err error) error {
	return fmt.Errorf("cannot reach the kyotee engine at %s — is it running? Start one with `kyotee serve`, or point KYOTEE_URL at it (%v)", baseURL, err)
}

type remoteClient struct {
	baseURL string
	http    *http.Client
}

func newRemoteClient(baseURL string) *remoteClient {
	return &remoteClient{baseURL: baseURL, http: &http.Client{Timeout: 30 * time.Second}}
}

func (c *remoteClient) getJSON(path string, v any) error {
	resp, err := c.http.Get(c.baseURL + path)
	if err != nil {
		return errNoEngine(c.baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return apiErrFrom(resp)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

func apiErrFrom(resp *http.Response) error {
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	var body struct {
		Error string `json:"error"`
	}
	if json.Unmarshal(raw, &body) == nil && body.Error != "" {
		return fmt.Errorf("engine: %s (HTTP %d)", body.Error, resp.StatusCode)
	}
	return fmt.Errorf("engine: HTTP %d", resp.StatusCode)
}

// submit POSTs a task and returns its ID.
func (c *remoteClient) submit(text string, ov receptionist.Overrides) (string, error) {
	payload, _ := json.Marshal(map[string]any{"text": text, "overrides": ov})
	resp, err := c.http.Post(c.baseURL+"/v1/tasks", "application/json", bytes.NewReader(payload))
	if err != nil {
		return "", errNoEngine(c.baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		return "", apiErrFrom(resp)
	}
	var out struct {
		TaskID string `json:"task_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	return out.TaskID, nil
}

func (c *remoteClient) resume(taskID string) error {
	resp, err := c.http.Post(c.baseURL+"/v1/tasks/"+taskID+"/resume", "application/json", nil)
	if err != nil {
		return errNoEngine(c.baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusAccepted {
		return apiErrFrom(resp)
	}
	return nil
}

// askResult is the stable --json contract (spec 09 §4).
type askResult struct {
	TaskID       string        `json:"task_id"`
	Answer       string        `json:"answer"`
	Strategy     string        `json:"strategy"`
	Consensus    *askConsensus `json:"consensus,omitempty"`
	Dissent      []string      `json:"dissent,omitempty"`
	TotalCostUSD float64       `json:"total_cost_usd"`
	TotalTokens  int           `json:"total_tokens"`
}

type askConsensus struct {
	Reached    bool   `json:"reached"`
	Method     string `json:"method"`
	RoundsUsed int    `json:"rounds_used"`
}

// errBudgetNoAnswer marks budget exhaustion that produced no usable answer —
// a non-zero exit per spec 09.
var errBudgetNoAnswer = errors.New("budget exhausted before any answer was produced")

// wait consumes the task's SSE stream (replay-then-tail) until it
// terminates, writing a terse per-stage progress log to progress (stderr)
// and returning the collected result.
func (c *remoteClient) wait(taskID string, progress io.Writer) (*askResult, error) {
	// No overall timeout: council runs can be slow. The engine's ": ping"
	// heartbeat keeps the connection alive; ctrl-C aborts the CLI.
	httpClient := &http.Client{}
	resp, err := httpClient.Get(c.baseURL + "/v1/tasks/" + taskID + "/events")
	if err != nil {
		return nil, errNoEngine(c.baseURL, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, apiErrFrom(resp)
	}

	res := &askResult{TaskID: taskID}
	var terminalErr error
	finalReason := ""

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	current := ""
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "event: ") {
			current = strings.TrimPrefix(line, "event: ")
			if current == "done" {
				break
			}
			continue
		}
		if !strings.HasPrefix(line, "data: ") || current == "done" {
			continue
		}
		var ev events.Event
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			continue
		}
		c.progressLine(progress, ev)

		p := ev.Payload
		switch ev.Kind {
		case events.KindTaskRouted:
			res.Strategy, _ = p["strategy"].(string)
		case events.KindCouncilConsensus:
			reached, _ := p["reached"].(bool)
			method, _ := p["method"].(string)
			rounds := 0
			if f, ok := p["rounds_used"].(float64); ok {
				rounds = int(f)
			}
			res.Consensus = &askConsensus{Reached: reached, Method: method, RoundsUsed: rounds}
		case events.KindTaskFinal:
			res.Answer, _ = p["text"].(string)
			if f, ok := p["total_cost_usd"].(float64); ok {
				res.TotalCostUSD = f
			}
			if f, ok := p["total_tokens"].(float64); ok {
				res.TotalTokens = int(f)
			}
			finalReason, _ = p["reason"].(string)
		case events.KindError:
			if t, _ := p["terminal"].(bool); t {
				msg, _ := p["message"].(string)
				terminalErr = fmt.Errorf("engine: %s", msg)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return res, fmt.Errorf("event stream interrupted: %w", err)
	}
	if terminalErr != nil {
		return res, terminalErr
	}
	if finalReason == "budget_exhausted" && strings.TrimSpace(res.Answer) == "" {
		return res, errBudgetNoAnswer
	}

	// Dissent lives in the persisted state's Meta, not in the event stream.
	var st pipeline.State
	if err := c.getJSON("/v1/tasks/"+taskID, &st); err == nil {
		if d := strings.TrimSpace(st.Meta["council.dissent"]); d != "" {
			for _, part := range strings.Split(d, "\n\n") {
				if part = strings.TrimSpace(part); part != "" {
					res.Dissent = append(res.Dissent, part)
				}
			}
		}
		if res.Strategy == "" {
			res.Strategy = st.Meta["strategy"]
		}
	}
	return res, nil
}

// progressLine writes the terse stderr progress log (stdout stays clean for
// the answer).
func (c *remoteClient) progressLine(w io.Writer, ev events.Event) {
	p := ev.Payload
	switch ev.Kind {
	case events.KindTaskClassified:
		fmt.Fprintf(w, "· classified  %v/%v tools:%v\n", p["domain"], p["complexity"], p["tool_need"])
	case events.KindTaskRouted:
		fmt.Fprintf(w, "· routed      %v (%v) budget $%v\n", p["strategy"], p["thinking"], p["limit_usd"])
	case events.KindStageStart:
		fmt.Fprintf(w, "· stage       %v…\n", p["stage"])
	case events.KindStageEnd:
		fmt.Fprintf(w, "· stage done  %v (spent $%.4f)\n", p["stage"], num(p["spent_usd"]))
	case events.KindThinkingMode:
		fmt.Fprintf(w, "· thinking    %v — %v\n", p["mode"], p["reason"])
	case events.KindThinkingToolChk:
		fmt.Fprintf(w, "· tool-check  %v %v\n", p["verdict"], p["tools"])
	case events.KindToolCall:
		fmt.Fprintf(w, "· tool        %v %v\n", p["name"], p["input"])
	case events.KindCouncilVote:
		fmt.Fprintf(w, "· vote        %v → %v (%.2f)\n", p["model"], p["choice"], num(p["confidence"]))
	case events.KindCouncilConsensus:
		fmt.Fprintf(w, "· consensus   reached=%v method=%v rounds=%v\n", p["reached"], p["method"], p["rounds_used"])
	case events.KindBudgetWarn:
		if reason, ok := p["reason"].(string); ok {
			fmt.Fprintf(w, "! budget      %s\n", reason)
		} else {
			fmt.Fprintf(w, "! budget      %.0f%% of $%.2f\n", num(p["pct"])*100, num(p["limit_usd"]))
		}
	case events.KindError:
		fmt.Fprintf(w, "! error       %v\n", p["message"])
	}
}

func num(v any) float64 {
	f, _ := v.(float64)
	return f
}

// runRemoteAsk implements `kyotee ask` against a running engine: submit,
// optionally wait, print the answer (or the stable JSON contract) to stdout.
func runRemoteAsk(baseURL, prompt string, ov receptionist.Overrides, doWait, jsonOut bool, stdout, stderr io.Writer) error {
	client := newRemoteClient(baseURL)
	taskID, err := client.submit(prompt, ov)
	if err != nil {
		return err
	}
	if !doWait {
		fmt.Fprintln(stdout, taskID)
		return nil
	}
	return waitAndPrint(client, taskID, jsonOut, stdout, stderr)
}

// runRemoteResume implements `kyotee resume <task_id>`.
func runRemoteResume(baseURL, taskID string, doWait, jsonOut bool, stdout, stderr io.Writer) error {
	client := newRemoteClient(baseURL)
	if err := client.resume(taskID); err != nil {
		return err
	}
	if !doWait {
		fmt.Fprintln(stdout, taskID)
		return nil
	}
	return waitAndPrint(client, taskID, jsonOut, stdout, stderr)
}

func waitAndPrint(client *remoteClient, taskID string, jsonOut bool, stdout, stderr io.Writer) error {
	res, err := client.wait(taskID, stderr)
	if err != nil {
		return err
	}
	if jsonOut {
		enc := json.NewEncoder(stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(res)
	}
	fmt.Fprintln(stdout, res.Answer)
	if len(res.Dissent) > 0 {
		fmt.Fprintln(stderr, "— dissent noted:")
		for _, d := range res.Dissent {
			fmt.Fprintln(stderr, "  ·", d)
		}
	}
	fmt.Fprintf(stderr, "— total cost $%.4f (%d tokens)\n", res.TotalCostUSD, res.TotalTokens)
	return nil
}

// runRemoteStatus implements `kyotee status <task_id>`: print the State
// snapshot as JSON.
func runRemoteStatus(baseURL, taskID string, stdout io.Writer) error {
	client := newRemoteClient(baseURL)
	var st json.RawMessage
	if err := client.getJSON("/v1/tasks/"+taskID, &st); err != nil {
		return err
	}
	var pretty bytes.Buffer
	if err := json.Indent(&pretty, st, "", "  "); err != nil {
		return err
	}
	fmt.Fprintln(stdout, pretty.String())
	return nil
}

// runRemoteProviders implements `kyotee providers`.
func runRemoteProviders(baseURL string, stdout io.Writer) error {
	client := newRemoteClient(baseURL)
	var provs []struct {
		Name        string  `json:"name"`
		Vendor      string  `json:"vendor"`
		Reasoning   bool    `json:"reasoning"`
		MaxContext  int     `json:"max_context"`
		InputPer1M  float64 `json:"input_usd_per_1m"`
		OutputPer1M float64 `json:"output_usd_per_1m"`
	}
	if err := client.getJSON("/v1/providers", &provs); err != nil {
		return err
	}
	tw := bufio.NewWriter(stdout)
	fmt.Fprintf(tw, "%-24s %-10s %-9s %-10s %s\n", "NAME", "VENDOR", "REASONING", "CONTEXT", "COST in/out per 1M")
	for _, p := range provs {
		fmt.Fprintf(tw, "%-24s %-10s %-9v %-10d $%.2f / $%.2f\n",
			p.Name, p.Vendor, p.Reasoning, p.MaxContext, p.InputPer1M, p.OutputPer1M)
	}
	return tw.Flush()
}
