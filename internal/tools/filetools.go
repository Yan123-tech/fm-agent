package tools

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"fm-agent/internal/safety"
	"fm-agent/internal/serve"
)

// --- read_file ---

type readFile struct {
	ws *safety.Workspace
}

// NewReadFile builds the read_file tool.
func NewReadFile(ws *safety.Workspace) Tool { return &readFile{ws} }

func (t *readFile) Spec() serve.Tool {
	return spec("read_file",
		"Read a text file. Lines are numbered. Use offset/limit for large files; at most "+strconv.Itoa(MaxReadLines)+" lines are returned.",
		map[string]any{
			"path":   strProp("File path relative to the workspace"),
			"offset": intProp("1-based first line to read"),
			"limit":  intProp("Max lines to read"),
		}, []string{"offset", "limit"})
}

type readArgs struct {
	Path   string `json:"path"`
	Offset int    `json:"offset"`
	Limit  int    `json:"limit"`
}

func (t *readFile) Describe(args json.RawMessage) string {
	var a readArgs
	_ = decodeArgs(args, &a)
	return "read_file " + a.Path
}

func (t *readFile) NeedsConfirm() bool { return false }

func (t *readFile) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a readArgs
	if err := decodeArgs(args, &a); err != nil {
		return "", err
	}
	abs, err := t.ws.Resolve(a.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	if isBinary(data) {
		return "", fmt.Errorf("%s looks like a binary file", a.Path)
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	start := 1
	if a.Offset > 1 {
		start = a.Offset
	}
	limit := MaxReadLines
	if a.Limit > 0 && a.Limit < limit {
		limit = a.Limit
	}
	var b strings.Builder
	n := 0
	for i := start - 1; i < len(lines) && n < limit; i++ {
		fmt.Fprintf(&b, "%6d\t%s\n", i+1, lines[i])
		n++
	}
	truncated := ""
	if start > 1 || start-1+n < len(lines) {
		truncated = fmt.Sprintf("[showing lines %d-%d of %d]\n", start, start-1+n, len(lines))
	}
	return truncated + b.String(), nil
}

func isBinary(data []byte) bool {
	const sniff = 512
	if len(data) < sniff {
		return strings.IndexByte(string(data), 0) >= 0
	}
	return strings.IndexByte(string(data[:sniff]), 0) >= 0
}

// --- write_file ---

type writeFile struct {
	ws *safety.Workspace
}

// NewWriteFile builds the write_file tool.
func NewWriteFile(ws *safety.Workspace) Tool { return &writeFile{ws} }

func (t *writeFile) Spec() serve.Tool {
	return spec("write_file",
		"Create or fully overwrite a file with the given content.",
		map[string]any{
			"path":    strProp("File path relative to the workspace"),
			"content": strProp("Full new file content"),
		}, nil)
}

type writeArgs struct {
	Path    string `json:"path"`
	Content string `json:"content"`
}

func (t *writeFile) Describe(args json.RawMessage) string {
	var a writeArgs
	_ = decodeArgs(args, &a)
	action := "Create"
	if _, err := t.ws.Resolve(a.Path); err == nil {
		if _, statErr := os.Stat(mustResolve(t.ws, a.Path)); statErr == nil {
			action = "Overwrite"
		}
	}
	return fmt.Sprintf("%s %s (%d bytes)", action, a.Path, len(a.Content))
}

func mustResolve(ws *safety.Workspace, p string) string {
	abs, err := ws.Resolve(p)
	if err != nil {
		return p
	}
	return abs
}

func (t *writeFile) NeedsConfirm() bool { return true }

func (t *writeFile) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a writeArgs
	if err := decodeArgs(args, &a); err != nil {
		return "", err
	}
	abs, err := t.ws.Resolve(a.Path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(abs, []byte(a.Content), 0o644); err != nil {
		return "", err
	}
	return fmt.Sprintf("Wrote %s (%d bytes)", a.Path, len(a.Content)), nil
}

// --- edit_file ---

type editFile struct {
	ws *safety.Workspace
}

// NewEditFile builds the edit_file tool.
func NewEditFile(ws *safety.Workspace) Tool { return &editFile{ws} }

func (t *editFile) Spec() serve.Tool {
	return spec("edit_file",
		"Replace exactly one occurrence of old_string with new_string in a file. old_string must be unique and match verbatim.",
		map[string]any{
			"path":       strProp("File path relative to the workspace"),
			"old_string": strProp("Exact text to replace (must appear exactly once)"),
			"new_string": strProp("Replacement text"),
		}, nil)
}

type editArgs struct {
	Path      string `json:"path"`
	OldString string `json:"old_string"`
	NewString string `json:"new_string"`
}

func (t *editFile) Describe(args json.RawMessage) string {
	var a editArgs
	_ = decodeArgs(args, &a)
	return fmt.Sprintf("edit_file %s: replace %d chars with %d chars", a.Path, len(a.OldString), len(a.NewString))
}

func (t *editFile) NeedsConfirm() bool { return true }

func (t *editFile) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a editArgs
	if err := decodeArgs(args, &a); err != nil {
		return "", err
	}
	abs, err := t.ws.Resolve(a.Path)
	if err != nil {
		return "", err
	}
	data, err := os.ReadFile(abs)
	if err != nil {
		return "", err
	}
	content := string(data)
	count := strings.Count(content, a.OldString)
	switch {
	case count == 0:
		return "", fmt.Errorf("old_string not found in %s", a.Path)
	case count > 1:
		return "", fmt.Errorf("old_string appears %d times in %s; provide more context to make it unique", count, a.Path)
	}
	updated := strings.Replace(content, a.OldString, a.NewString, 1)
	if err := os.WriteFile(abs, []byte(updated), 0o644); err != nil {
		return "", err
	}
	oldLines := strings.Count(a.OldString, "\n") + 1
	return fmt.Sprintf("Edited %s: replaced %d line(s)", a.Path, oldLines), nil
}

// --- list_files ---

type listFiles struct {
	ws *safety.Workspace
}

// NewListFiles builds the list_files tool.
func NewListFiles(ws *safety.Workspace) Tool { return &listFiles{ws} }

func (t *listFiles) Spec() serve.Tool {
	return spec("list_files",
		"List workspace files. BEST: call with empty arguments {} to get the COMPLETE recursive "+
			"file tree including all subdirectories. Use pattern only to narrow down: "+
			"\"*.go\" = root level only, \"internal/**\" = one subtree, \"**/*_test.go\" = matching files everywhere.",
		map[string]any{
			"pattern": strProp("Optional glob (*, ?, **). Omit for the full recursive tree."),
		}, []string{"pattern"})
}

type listArgs struct {
	Pattern string `json:"pattern"`
}

func (t *listFiles) Describe(args json.RawMessage) string {
	var a listArgs
	_ = decodeArgs(args, &a)
	return "list_files " + a.Pattern
}

func (t *listFiles) NeedsConfirm() bool { return false }

func (t *listFiles) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a listArgs
	if err := decodeArgs(args, &a); err != nil {
		return "", err
	}
	if a.Pattern == "" {
		a.Pattern = "**"
	}
	root := t.ws.Roots()[0]
	var matches []string
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		if matchGlob(a.Pattern, filepath.ToSlash(rel)) && len(matches) < MaxListResults {
			matches = append(matches, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(matches) == 0 {
		return "No files matched.", nil
	}
	sortStrings(matches)
	return strings.Join(matches, "\n"), nil
}

// matchGlob matches slash-separated paths against a glob with ** support.
func matchGlob(pattern, name string) bool {
	return matchSegments(strings.Split(pattern, "/"), strings.Split(name, "/"))
}

func matchSegments(pat, name []string) bool {
	if len(pat) == 0 {
		return len(name) == 0
	}
	if pat[0] == "**" {
		for i := 0; i <= len(name); i++ {
			if matchSegments(pat[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], name[0])
	return err == nil && ok && matchSegments(pat[1:], name[1:])
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// --- search ---

type search struct {
	ws *safety.Workspace
}

// NewSearch builds the search tool.
func NewSearch(ws *safety.Workspace) Tool { return &search{ws} }

func (t *search) Spec() serve.Tool {
	return spec("search",
		"Search file contents by regular expression, recursively through the whole workspace "+
			"(including all subdirectories). Returns matching lines with file:line. .git is skipped.",
		map[string]any{
			"pattern": strProp("RE2 regular expression"),
			"path":    strProp("Optional file or subdirectory to limit the search to"),
		}, []string{"path"})
}

type searchArgs struct {
	Pattern string `json:"pattern"`
	Path    string `json:"path"`
}

func (t *search) Describe(args json.RawMessage) string {
	var a searchArgs
	_ = decodeArgs(args, &a)
	return "search /" + a.Pattern + "/"
}

func (t *search) NeedsConfirm() bool { return false }

func (t *search) Execute(_ context.Context, args json.RawMessage) (string, error) {
	var a searchArgs
	if err := decodeArgs(args, &a); err != nil {
		return "", err
	}
	re, err := regexp.Compile(a.Pattern)
	if err != nil {
		return "", fmt.Errorf("invalid regexp: %w", err)
	}
	start := t.ws.Roots()[0]
	if a.Path != "" {
		abs, err := t.ws.Resolve(a.Path)
		if err != nil {
			return "", err
		}
		start = abs
	}
	var hits []string
	err = filepath.WalkDir(start, func(p string, d os.DirEntry, err error) error {
		if err != nil || len(hits) >= MaxSearchResults {
			return nil
		}
		if d.IsDir() {
			if d.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		data, err := os.ReadFile(p)
		if err != nil || isBinary(data) {
			return nil
		}
		root := t.ws.Roots()[0]
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return nil
		}
		for i, line := range strings.Split(string(data), "\n") {
			if len(hits) >= MaxSearchResults {
				return nil
			}
			if re.MatchString(line) {
				hits = append(hits, fmt.Sprintf("%s:%d: %s", filepath.ToSlash(rel), i+1, truncateLine(line)))
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(hits) == 0 {
		return "No matches.", nil
	}
	more := ""
	if len(hits) >= MaxSearchResults {
		more = fmt.Sprintf("[stopped at %d matches]\n", MaxSearchResults)
	}
	return more + strings.Join(hits, "\n"), nil
}

func truncateLine(s string) string {
	const max = 200
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
