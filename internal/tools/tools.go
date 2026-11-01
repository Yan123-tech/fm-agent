// Package tools implements the agent's callable tool set. Every tool
// declares an OpenAI-style function spec and an executor confined to a
// safety.Workspace.
package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"

	"fm-agent/internal/safety"
	"fm-agent/internal/serve"
)

const (
	// MaxReadLines caps how many lines read_file returns in one call.
	MaxReadLines = 200
	// MaxOutputChars caps any tool result passed back to the model.
	MaxOutputChars = 4000
	// MaxListResults caps list_files matches.
	MaxListResults = 100
	// MaxSearchResults caps search matches.
	MaxSearchResults = 50
)

// Tool is a single callable agent tool.
type Tool interface {
	// Spec is the function descriptor sent to the model.
	Spec() serve.Tool
	// Describe renders a short human-readable summary of a call, for
	// confirmation prompts.
	Describe(args json.RawMessage) string
	// NeedsConfirm reports whether the call must be approved by the user.
	NeedsConfirm() bool
	// Execute runs the tool; the returned string is fed back to the model.
	Execute(ctx context.Context, args json.RawMessage) (string, error)
}

// Registry holds the tool set and enforces confirmations.
type Registry struct {
	ws       *safety.Workspace
	prompter safety.Prompter

	byName map[string]Tool
	order  []string
}

// NewRegistry builds a registry with the standard tool set.
func NewRegistry(ws *safety.Workspace, prompter safety.Prompter) *Registry {
	r := &Registry{ws: ws, prompter: prompter, byName: map[string]Tool{}}
	r.add(NewReadFile(ws))
	r.add(NewWriteFile(ws))
	r.add(NewEditFile(ws))
	r.add(NewListFiles(ws))
	r.add(NewSearch(ws))
	r.add(NewRunCommand(ws))
	r.add(NewAskUser(prompter))
	return r
}

func (r *Registry) add(t Tool) {
	name := t.Spec().Function.Name
	if _, dup := r.byName[name]; !dup {
		r.order = append(r.order, name)
	}
	r.byName[name] = t
}

// Specs returns the tool descriptors, sorted by name for determinism.
func (r *Registry) Specs() []serve.Tool {
	names := append([]string(nil), r.order...)
	sort.Strings(names)
	out := make([]serve.Tool, 0, len(names))
	for _, n := range names {
		out = append(out, r.byName[n].Spec())
	}
	return out
}

// Run executes one tool call, asking for confirmation when required.
// The boolean reports whether the call was approved.
func (r *Registry) Run(ctx context.Context, tc serve.ToolCall) (result string, approved bool, err error) {
	t, ok := r.byName[tc.Function.Name]
	if !ok {
		return "", false, fmt.Errorf("unknown tool %q", tc.Function.Name)
	}
	args := json.RawMessage(tc.Function.Arguments)
	if t.NeedsConfirm() && !r.prompter.Confirm(t.Describe(args)) {
		return "The user declined this action; do not retry it.", true, nil
	}
	out, err := t.Execute(ctx, args)
	if err != nil {
		out = "Error: " + err.Error()
	}
	return truncate(out, MaxOutputChars), true, nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	half := max / 2
	return s[:half] + fmt.Sprintf("\n… [truncated %d bytes] …\n", len(s)-max) + s[len(s)-half:]
}

// --- JSON helpers ---

func decodeArgs(args json.RawMessage, v any) error {
	if len(args) == 0 {
		args = []byte("{}")
	}
	if err := json.Unmarshal(args, v); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

// spec builds a serve.Tool from a name, description and flat property list.
// Every property is required unless listed in optional.
func spec(name, description string, props map[string]any, optional []string) serve.Tool {
	propOrder := make([]string, 0, len(props))
	properties := map[string]any{}
	for k, v := range props {
		propOrder = append(propOrder, k)
		properties[k] = v
	}
	sort.Strings(propOrder)
	required := make([]string, 0, len(propOrder))
	opt := map[string]bool{}
	for _, o := range optional {
		opt[o] = true
	}
	for _, k := range propOrder {
		if !opt[k] {
			required = append(required, k)
		}
	}
	return serve.Tool{
		Type: "function",
		Function: serve.FunctionSpec{
			Name:        name,
			Description: description,
			Parameters: map[string]any{
				"type":       "object",
				"properties": properties,
				"required":   required,
			},
		},
	}
}

func strProp(desc string) map[string]any {
	return map[string]any{"type": "string", "description": desc}
}

func intProp(desc string) map[string]any {
	return map[string]any{"type": "integer", "description": desc}
}
