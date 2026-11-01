package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fm-agent/internal/safety"
	"fm-agent/internal/serve"
)

type stubPrompter struct{ confirm bool }

func (s stubPrompter) Confirm(string) bool { return s.confirm }
func (s stubPrompter) Ask(q string) string { return "answer to: " + q }

func setup(t *testing.T) (*Registry, string) {
	t.Helper()
	dir := t.TempDir()
	ws, err := safety.NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	return NewRegistry(ws, stubPrompter{confirm: true}), dir
}

func raw(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestReadFileNumberedAndTruncated(t *testing.T) {
	reg, dir := setup(t)
	var lines []string
	for i := 1; i <= 500; i++ {
		lines = append(lines, strings.Repeat("x", 10))
	}
	if err := os.WriteFile(filepath.Join(dir, "big.txt"), []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _, err := reg.Run(context.Background(), call("read_file", map[string]any{"path": "big.txt"}))
	if err != nil {
		t.Fatal(err)
	}
	resLines := strings.Split(strings.TrimSuffix(res, "\n"), "\n")
	if !strings.Contains(resLines[0], "showing lines 1-200 of 500") {
		t.Errorf("missing truncation notice: %q", resLines[0])
	}
	if got := len(resLines) - 1; got != MaxReadLines {
		t.Errorf("returned %d lines, want %d", got, MaxReadLines)
	}
}

func TestWriteFileConfinedAndConfirmed(t *testing.T) {
	reg, dir := setup(t)
	res, approved, err := reg.Run(context.Background(), call("write_file", map[string]any{
		"path": "a/b.txt", "content": "hello",
	}))
	if err != nil || !approved {
		t.Fatalf("err=%v approved=%v", err, approved)
	}
	data, err := os.ReadFile(filepath.Join(dir, "a", "b.txt"))
	if err != nil || string(data) != "hello" {
		t.Errorf("data=%q err=%v", data, err)
	}

	res, _, _ = reg.Run(context.Background(), call("write_file", map[string]any{
		"path": "../../etc/evil", "content": "x",
	}))
	if !strings.Contains(res, "outside the workspace") {
		t.Errorf("expected confinement error, got: %s", res)
	}
}

func TestWriteFileDeclined(t *testing.T) {
	dir := t.TempDir()
	ws, _ := safety.NewWorkspace(dir)
	reg := NewRegistry(ws, stubPrompter{confirm: false})
	res, _, _ := reg.Run(context.Background(), call("write_file", map[string]any{
		"path": "x.txt", "content": "x",
	}))
	if _, statErr := os.Stat(filepath.Join(dir, "x.txt")); statErr == nil {
		t.Error("file must not be written when declined")
	}
	if !strings.Contains(res, "declined") {
		t.Errorf("expected decline note, got: %s", res)
	}
}

func TestEditFileUniqueness(t *testing.T) {
	reg, dir := setup(t)
	content := "foo\nbar\nfoo\n"
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	res, _, _ := reg.Run(context.Background(), call("edit_file", map[string]any{
		"path": "f.txt", "old_string": "foo", "new_string": "baz",
	}))
	if !strings.Contains(res, "appears 2 times") {
		t.Errorf("expected uniqueness error, got: %s", res)
	}

	res, _, _ = reg.Run(context.Background(), call("edit_file", map[string]any{
		"path": "f.txt", "old_string": "bar", "new_string": "baz",
	}))
	if !strings.Contains(res, "Edited") {
		t.Errorf("expected success, got: %s", res)
	}
	data, _ := os.ReadFile(filepath.Join(dir, "f.txt"))
	if string(data) != "foo\nbaz\nfoo\n" {
		t.Errorf("content = %q", data)
	}

	res, _, _ = reg.Run(context.Background(), call("edit_file", map[string]any{
		"path": "f.txt", "old_string": "missing", "new_string": "x",
	}))
	if !strings.Contains(res, "not found") {
		t.Errorf("expected not-found error, got: %s", res)
	}
}

func TestListFilesGlob(t *testing.T) {
	reg, dir := setup(t)
	for _, p := range []string{"main.go", "internal/a/x.go", "internal/b/y.go", "README.md"} {
		full := filepath.Join(dir, filepath.FromSlash(p))
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("package x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	res, _, _ := reg.Run(context.Background(), call("list_files", map[string]any{"pattern": "internal/**/*.go"}))
	if !strings.Contains(res, "internal/a/x.go") || !strings.Contains(res, "internal/b/y.go") {
		t.Errorf("missing matches:\n%s", res)
	}
	if strings.Contains(res, "main.go") || strings.Contains(res, "README") {
		t.Errorf("unexpected matches:\n%s", res)
	}

	// No pattern: full recursive tree.
	res, _, _ = reg.Run(context.Background(), call("list_files", map[string]any{}))
	for _, want := range []string{"main.go", "internal/a/x.go", "internal/b/y.go", "README.md"} {
		if !strings.Contains(res, want) {
			t.Errorf("no-pattern listing missing %s:\n%s", want, res)
		}
	}
}

func TestSearchRegexp(t *testing.T) {
	reg, dir := setup(t)
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package main\n// TODO: fix\nfunc main() {}\n"), 0o644)
	os.MkdirAll(filepath.Join(dir, ".git"), 0o755)
	os.WriteFile(filepath.Join(dir, ".git", "cfg"), []byte("TODO\n"), 0o644)

	res, _, _ := reg.Run(context.Background(), call("search", map[string]any{"pattern": "TODO"}))
	if !strings.Contains(res, "a.go:2") {
		t.Errorf("missing hit:\n%s", res)
	}
	if strings.Contains(res, "cfg") {
		t.Errorf(".git must be skipped:\n%s", res)
	}
}

func TestSearchBadRegexp(t *testing.T) {
	reg, _ := setup(t)
	res, _, _ := reg.Run(context.Background(), call("search", map[string]any{"pattern": "([", "path": "."}))
	if !strings.Contains(res, "invalid regexp") {
		t.Errorf("expected regexp error, got: %s", res)
	}
}

func TestRunCommand(t *testing.T) {
	reg, dir := setup(t)
	res, _, _ := reg.Run(context.Background(), call("run_command", map[string]any{"command": "pwd && echo hi"}))
	if !strings.Contains(res, dir) || !strings.Contains(res, "hi") {
		t.Errorf("unexpected output: %s", res)
	}
	res, _, _ = reg.Run(context.Background(), call("run_command", map[string]any{"command": "exit 3"}))
	if !strings.Contains(res, "exit status 3") {
		t.Errorf("expected exit status note, got: %s", res)
	}
}

func TestUnknownTool(t *testing.T) {
	reg, _ := setup(t)
	_, _, err := reg.Run(context.Background(), call("nope", map[string]any{}))
	if err == nil {
		t.Error("expected error for unknown tool")
	}
}

func call(name string, args map[string]any) serve.ToolCall {
	b, _ := json.Marshal(args)
	var tc serve.ToolCall
	tc.ID = "call_" + name
	tc.Type = "function"
	tc.Function.Name = name
	tc.Function.Arguments = string(b)
	return tc
}
