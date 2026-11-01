// Package safety provides workspace confinement and user interaction
// primitives for the agent.
package safety

import (
	"bufio"
	"fmt"
	"io"
	"path/filepath"
	"strings"
)

// Workspace confines all file access to a set of allowed root directories.
type Workspace struct {
	roots []string
}

// NewWorkspace builds a confinement set from the given paths. Each root is
// made absolute and symlink-resolved; it must exist.
func NewWorkspace(roots ...string) (*Workspace, error) {
	if len(roots) == 0 {
		return nil, fmt.Errorf("workspace requires at least one root")
	}
	w := &Workspace{}
	for _, r := range roots {
		abs, err := filepath.Abs(r)
		if err != nil {
			return nil, err
		}
		resolved, err := filepath.EvalSymlinks(abs)
		if err != nil {
			return nil, fmt.Errorf("workspace root %s: %w", r, err)
		}
		w.roots = append(w.roots, resolved)
	}
	return w, nil
}

// Roots returns the absolute resolved root directories.
func (w *Workspace) Roots() []string {
	return append([]string(nil), w.roots...)
}

// Resolve maps a possibly relative path into the workspace, enforcing that
// the resolved absolute path (with symlinks evaluated when the target
// exists) stays inside one of the roots.
func (w *Workspace) Resolve(p string) (string, error) {
	var abs string
	if filepath.IsAbs(p) {
		abs = filepath.Clean(p)
	} else {
		abs = filepath.Join(w.roots[0], p)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	for _, root := range w.roots {
		if within(root, abs) {
			return abs, nil
		}
	}
	return "", fmt.Errorf("path %q is outside the workspace", p)
}

// Rel returns p relative to the primary root for display purposes.
func (w *Workspace) Rel(abs string) string {
	rel, err := filepath.Rel(w.roots[0], abs)
	if err != nil || strings.HasPrefix(rel, "..") {
		return abs
	}
	return rel
}

func within(root, p string) bool {
	if p == root {
		return true
	}
	rel, err := filepath.Rel(root, p)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// Prompter is how tools and the agent ask the user questions.
type Prompter interface {
	// Confirm asks the user to approve a described action; false means deny.
	Confirm(description string) bool
	// Ask poses a question and returns the user's free-text answer.
	Ask(question string) string
}

// IOPrompter implements Prompter over stdin/stdout. It shares its buffered
// reader with the REPL so that input buffered while confirming is not lost.
type IOPrompter struct {
	In  *bufio.Reader
	Out io.Writer
}

// NewIOPrompter builds a prompter over a shared buffered reader.
func NewIOPrompter(in *bufio.Reader, out io.Writer) *IOPrompter {
	return &IOPrompter{In: in, Out: out}
}

// Confirm prints the description and reads y/n (default no).
func (p *IOPrompter) Confirm(description string) bool {
	fmt.Fprintf(p.Out, "\n  %s\n  Proceed? [y/N]: ", description)
	answer, err := p.In.ReadString('\n')
	if err != nil && answer == "" {
		return false
	}
	return strings.EqualFold(strings.TrimSpace(answer), "y")
}

// Ask prints the question and reads a single line.
func (p *IOPrompter) Ask(question string) string {
	fmt.Fprintf(p.Out, "\n  %s\n  > ", question)
	answer, _ := p.In.ReadString('\n')
	return strings.TrimSpace(answer)
}

// AutoApprove always confirms; used with --yes.
type AutoApprove struct{}

// Confirm implements Prompter.
func (AutoApprove) Confirm(string) bool { return true }

// Ask implements Prompter.
func (AutoApprove) Ask(string) string { return "" }
