package thinking

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/stukennedy/kyotee/internal/provider"
)

// Tool is an executable capability the solver can call mid-generation.
type Tool interface {
	Def() provider.ToolDef
	Exec(ctx context.Context, input map[string]any) (string, error)
}

// ToolRegistry holds the provider-agnostic tools available to solvers.
type ToolRegistry struct {
	m map[string]Tool
}

func NewToolRegistry(tools ...Tool) *ToolRegistry {
	r := &ToolRegistry{m: map[string]Tool{}}
	for _, t := range tools {
		r.Register(t)
	}
	return r
}

func (r *ToolRegistry) Register(t Tool) { r.m[t.Def().Name] = t }

func (r *ToolRegistry) Get(name string) (Tool, bool) {
	t, ok := r.m[name]
	return t, ok
}

func (r *ToolRegistry) Names() []string {
	out := make([]string, 0, len(r.m))
	for n := range r.m {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func (r *ToolRegistry) Defs() []provider.ToolDef {
	defs := make([]provider.ToolDef, 0, len(r.m))
	for _, n := range r.Names() {
		defs = append(defs, r.m[n].Def())
	}
	return defs
}

// DefsFor returns definitions for the named subset (unknown names skipped).
func (r *ToolRegistry) DefsFor(names []string) []provider.ToolDef {
	var defs []provider.ToolDef
	for _, n := range names {
		if t, ok := r.m[strings.TrimSpace(n)]; ok {
			defs = append(defs, t.Def())
		}
	}
	return defs
}

// FuncTool adapts a plain function into a Tool.
type FuncTool struct {
	Definition provider.ToolDef
	Fn         func(ctx context.Context, input map[string]any) (string, error)
}

func (f *FuncTool) Def() provider.ToolDef { return f.Definition }

func (f *FuncTool) Exec(ctx context.Context, input map[string]any) (string, error) {
	if f.Fn == nil {
		return "", fmt.Errorf("tool %s has no implementation", f.Definition.Name)
	}
	return f.Fn(ctx, input)
}
