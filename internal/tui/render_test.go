package tui

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"fm-agent/internal/agent"
	"fm-agent/internal/safety"
	"fm-agent/internal/session"
	"fm-agent/internal/tools"
)

func TestRealRendererTyping(t *testing.T) {
	srv := stubServe(t, `{"role":"assistant","content":"done"}`)
	dir := t.TempDir()
	ws, _ := safety.NewWorkspace(dir)
	reg := tools.NewRegistry(ws, safety.AutoApprove{})
	ag := agent.New(srv, reg, ws.Roots(), agent.Options{Out: io.Discard})
	store, _ := session.NewStoreAt(t.TempDir())
	m := New(ag, store, "test")
	out := &bytes.Buffer{}
	prog := tea.NewProgram(m, tea.WithInput(nil), tea.WithOutput(out))
	m.SetProgram(prog)
	ag.SetOut(m.StreamWriter())
	ag.SetToolPrinter(m.ToolPrinter())
	ag.SetToolEventPrinter(m.ToolEventPrinter())
	reg.SetPrompter(NewPrompter(prog))

	done := make(chan struct{})
	go func() { _, _ = prog.Run(); close(done) }()
	prog.Send(tea.WindowSizeMsg{Width: 80, Height: 24})
	time.Sleep(200 * time.Millisecond)
	prog.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("abc")})
	time.Sleep(400 * time.Millisecond)
	prog.Send(tea.Quit())
	<-done

	s := out.String()
	tail := s[max(0, len(s)-300):]
	t.Logf("TAIL: %q", tail)
	if !strings.Contains(tail, "abc") {
		t.Error("typed text missing from rendered output")
	}
}
