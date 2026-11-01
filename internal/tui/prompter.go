package tui

import (
	tea "github.com/charmbracelet/bubbletea"

	"fm-agent/internal/safety"
)

// Prompter is a safety.Prompter that renders confirmations inside the
// bubbletea event loop instead of reading stdin directly.
type Prompter struct {
	prog *tea.Program
}

// NewPrompter builds the prompter for a program.
func NewPrompter(prog *tea.Program) safety.Prompter {
	return &Prompter{prog: prog}
}

// Confirm asks via the UI and blocks until the user answers.
func (p *Prompter) Confirm(description string) bool {
	resp := make(chan bool, 1)
	p.prog.Send(confirmMsg{desc: description, resp: resp})
	return <-resp
}

// Ask asks via the UI; the user types the answer into the input box.
func (p *Prompter) Ask(question string) string {
	resp := make(chan string, 1)
	p.prog.Send(askMsg{question: question, resp: resp})
	return <-resp
}

// StreamWriter returns the io.Writer the agent streams its answer into;
// chunks are forwarded into the event loop as deltaMsg.
func (m *Model) StreamWriter() *StreamWriter { return &StreamWriter{prog: m.prog} }

// StreamWriter adapts agent streaming output to tea messages.
type StreamWriter struct {
	prog *tea.Program
}

// Write implements io.Writer.
func (w *StreamWriter) Write(p []byte) (int, error) {
	w.prog.Send(deltaMsg(p))
	return len(p), nil
}

// ToolPrinter returns the agent tool-start callback.
func (m *Model) ToolPrinter() func(string) {
	return func(name string) {
		m.prog.Send(toolStartMsg{name: name})
	}
}

// ToolEventPrinter returns the agent tool-done callback.
func (m *Model) ToolEventPrinter() func(name, result string) {
	return func(name, result string) {
		m.prog.Send(toolDoneMsg{name: name, result: result})
	}
}
