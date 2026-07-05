package thinking

import (
	"context"
	"encoding/json"

	"github.com/stukennedy/kyotee/internal/events"
	"github.com/stukennedy/kyotee/internal/provider"
)

// RunToolLoop executes a standard tool-use loop: call the model, execute any
// requested tools, feed results back, repeat until the model stops requesting
// tools or maxCalls tool executions have run. On hitting the cap it forces a
// final tool-free answer, so the loop always terminates with text.
// Returns the final response and the aggregate usage across all calls.
func RunToolLoop(ctx context.Context, p provider.Provider, req provider.Request, reg *ToolRegistry, maxCalls int, emit events.Emitter, stage string) (provider.Response, provider.Usage, error) {
	if maxCalls <= 0 {
		maxCalls = 5
	}
	var total provider.Usage
	callsUsed := 0

	for {
		resp, err := p.Generate(ctx, req)
		if err != nil {
			return provider.Response{}, total, err
		}
		total.Add(resp.Usage)

		calls := resp.ToolCalls()
		// Terminate on a text answer, on toolless requests, or after the
		// cap-forced ToolChoice="none" round (even if the model still tried
		// to call a tool) — the loop must never spin.
		if len(calls) == 0 || len(req.Tools) == 0 || req.ToolChoice == "none" {
			return resp, total, nil
		}

		// Record the assistant turn, then execute each requested tool.
		req.Messages = append(req.Messages, provider.Message{Role: "assistant", Content: resp.Content})
		var results []provider.Block
		for _, call := range calls {
			inputJSON, _ := json.Marshal(call.Input)
			emit(events.Event{
				Kind: events.KindToolCall, Stage: stage, Actor: p.Name(),
				Payload: map[string]any{"name": call.Name, "input": string(inputJSON)},
			})

			var output string
			var isErr bool
			if callsUsed >= maxCalls {
				output = "Tool call limit reached. Answer now with the information you already have."
				isErr = true
			} else if tool, ok := reg.Get(call.Name); ok {
				out, execErr := tool.Exec(ctx, call.Input)
				if execErr != nil {
					output, isErr = "tool error: "+execErr.Error(), true
				} else {
					output = out
				}
				callsUsed++
			} else {
				output, isErr = "unknown tool: "+call.Name, true
			}

			emit(events.Event{
				Kind: events.KindToolResult, Stage: stage, Actor: p.Name(),
				Payload: map[string]any{"name": call.Name, "output": truncateStr(output, 2000), "is_error": isErr},
			})
			results = append(results, provider.Block{Type: "tool_result", ToolResult: &provider.ToolResult{
				CallID: call.ID, Content: output, IsError: isErr,
			}})
		}
		req.Messages = append(req.Messages, provider.Message{Role: "tool", Content: results})

		if callsUsed >= maxCalls {
			// Force a final completion with no further tool use. Tools stay
			// DEFINED (histories containing tool_use/tool_result blocks are
			// rejected by vendors when the tools param is missing); the
			// adapter maps ToolChoice="none" to the vendor's knob.
			req.ToolChoice = "none"
		}
	}
}

func truncateStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
