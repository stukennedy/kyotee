package provider

import (
	"context"
	"sync"
)

// Fake is a scriptable in-memory Provider for tests and --mock mode.
// Responses are returned in order; when the script is exhausted the last
// response repeats. A ScriptFn, if set, takes precedence and can inspect
// the request (used to assert ReasoningEffort propagation, tool loops, etc.).
type Fake struct {
	ModelName  string
	VendorName string
	Script     []Response
	ScriptFn   func(call int, req Request) (Response, error)
	InUSD      float64 // cost per 1M input tokens
	OutUSD     float64 // cost per 1M output tokens

	mu       sync.Mutex
	Requests []Request // every request received, for assertions
}

func NewFake(name, vendor string, script ...Response) *Fake {
	return &Fake{ModelName: name, VendorName: vendor, Script: script}
}

// TextResponse builds a plain-text Response with the given usage.
func TextResponse(text string, inTok, outTok int) Response {
	return Response{
		Content:    []Block{{Type: "text", Text: text}},
		StopReason: "end_turn",
		Usage:      Usage{InputTokens: inTok, OutputTokens: outTok},
	}
}

func (f *Fake) Name() string   { return f.ModelName }
func (f *Fake) Vendor() string { return f.VendorName }

func (f *Fake) Capabilities() Capabilities {
	return Capabilities{Tools: true, Reasoning: true, MaxContext: 200000}
}

func (f *Fake) CostPer1M() (float64, float64) { return f.InUSD, f.OutUSD }

func (f *Fake) Generate(ctx context.Context, req Request) (Response, error) {
	f.mu.Lock()
	call := len(f.Requests)
	f.Requests = append(f.Requests, req)
	f.mu.Unlock()

	if err := ctx.Err(); err != nil {
		return Response{}, err
	}

	var resp Response
	var err error
	switch {
	case f.ScriptFn != nil:
		resp, err = f.ScriptFn(call, req)
	case len(f.Script) == 0:
		resp = TextResponse("(fake response)", 10, 10)
	case call < len(f.Script):
		resp = f.Script[call]
	default:
		resp = f.Script[len(f.Script)-1]
	}
	if err != nil {
		return Response{}, err
	}
	if resp.Usage.CostUSD == 0 {
		resp.Usage.CostUSD = CostFor(f, resp.Usage.InputTokens, resp.Usage.OutputTokens)
	}
	if req.Stream != nil {
		req.Stream(Delta{Type: "text", Text: resp.Text()})
		req.Stream(Delta{Type: "done"})
	}
	return resp, nil
}
