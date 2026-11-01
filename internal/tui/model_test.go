package tui

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"fm-agent/internal/agent"
	"fm-agent/internal/safety"
	"fm-agent/internal/serve"
	"fm-agent/internal/session"
	"fm-agent/internal/tools"
)

// stubServe replays canned messages (one per request) over a UDS HTTP server.
func stubServe(t *testing.T, canned ...string) *serve.Server {
	t.Helper()
	tmp, err := os.MkdirTemp("", "fmbt")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	sock := filepath.Join(tmp, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var count int
	requests := func() int { mu.Lock(); defer mu.Unlock(); return count }
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req serve.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		mu.Lock()
		idx := count
		count++
		mu.Unlock()
		msg := serve.Message{Role: "assistant", Content: "done"}
		if idx < len(canned) {
			_ = json.Unmarshal([]byte(canned[idx]), &msg)
		}
		delta := map[string]any{"role": "assistant", "content": msg.Content}
		if len(msg.ToolCalls) > 0 {
			delta["tool_calls"] = msg.ToolCalls
		}
		chunk, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"delta": delta, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 50, "completion_tokens": 5, "total_tokens": 55},
		})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	t.Cleanup(func() { t.Logf("stub requests: %d", requests()) })
	return serve.NewServer("fm", sock)
}

type env struct {
	m     *Model
	prog  *tea.Program
	dir   string
	done  chan struct{}
	store *session.Store
}

func newEnv(t *testing.T, canned ...string) *env {
	t.Helper()
	srv := stubServe(t, canned...)
	dir := t.TempDir()
	ws, err := safety.NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws, safety.AutoApprove{})
	ag := agent.New(srv, reg, ws.Roots(), agent.Options{Out: io.Discard})
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	m := New(ag, store, "test")
	prog := tea.NewProgram(m,
		tea.WithInput(nil),
		tea.WithOutput(io.Discard),
		tea.WithoutRenderer(),
	)
	m.SetProgram(prog)
	ag.SetOut(m.StreamWriter())
	ag.SetToolPrinter(m.ToolPrinter())
	ag.SetToolEventPrinter(m.ToolEventPrinter())
	reg.SetPrompter(NewPrompter(prog))

	e := &env{m: m, prog: prog, dir: dir, store: store, done: make(chan struct{})}
	go func() { _, _ = prog.Run(); close(e.done) }()
	prog.Send(tea.WindowSizeMsg{Width: 80, Height: 24})
	return e
}

func (e *env) snap() snapshot {
	resp := make(chan snapshot, 1)
	e.prog.Send(queryMsg{resp: resp})
	return <-resp
}

func (e *env) typeText(s string) {
	e.prog.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
}

func (e *env) enter() { e.prog.Send(tea.KeyMsg{Type: tea.KeyEnter}) }

func (e *env) waitFor(t *testing.T, timeout time.Duration, cond func(snapshot) bool, what string) snapshot {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s := e.snap()
		if cond(s) {
			return s
		}
		time.Sleep(15 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
	return snapshot{}
}

func (e *env) quit() {
	e.prog.Send(tea.Quit())
	<-e.done
}

func TestModelAnswerAndQuit(t *testing.T) {
	e := newEnv(t, `{"role":"assistant","content":"hello **world**"}`)
	e.typeText("hi")
	e.enter()
	s := e.waitFor(t, 5*time.Second, func(s snapshot) bool {
		for _, b := range s.blocks {
			if b.kind == blockAssistant && strings.Contains(b.rendered, "world") {
				return true
			}
		}
		return false
	}, "assistant answer")
	e.quit()

	var kinds []blockKind
	for _, b := range s.blocks {
		kinds = append(kinds, b.kind)
	}
	if kinds[0] != blockSys || kinds[1] != blockUser || kinds[2] != blockAssistant {
		t.Errorf("block order = %v", kinds)
	}
}

func TestModelConfirmation(t *testing.T) {
	tool := `{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"ok.txt\",\"content\":\"yes\"}"}}]}`
	e := newEnv(t, tool, `{"role":"assistant","content":"written"}`)
	e.typeText("create a file")
	e.enter()

	e.waitFor(t, 5*time.Second, func(s snapshot) bool { return s.confirm }, "confirm request")
	e.prog.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})

	e.waitFor(t, 5*time.Second, func(s snapshot) bool {
		_, err := os.Stat(filepath.Join(e.dir, "ok.txt"))
		return err == nil
	}, "file after y")
	s := e.waitFor(t, 5*time.Second, func(s snapshot) bool { return !s.running }, "run finished")

	var toolBlock *block
	for _, b := range s.blocks {
		if b.kind == blockTool {
			toolBlock = b
		}
	}
	if toolBlock == nil || toolBlock.pend || toolBlock.failed {
		t.Errorf("tool block = %+v", toolBlock)
	}
	e.quit()
}

func TestModelDeclineConfirmation(t *testing.T) {
	tool := `{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"no.txt\",\"content\":\"x\"}"}}]}`
	e := newEnv(t, tool, `{"role":"assistant","content":"declined"}`)
	e.typeText("create a file")
	e.enter()
	e.waitFor(t, 5*time.Second, func(s snapshot) bool { return s.confirm }, "confirm request")
	e.prog.Send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("n")})
	e.waitFor(t, 5*time.Second, func(s snapshot) bool { return !s.running }, "run finished")
	if _, err := os.Stat(filepath.Join(e.dir, "no.txt")); err == nil {
		t.Error("file must not exist after decline")
	}
	e.quit()
}

func TestModelSlashCommand(t *testing.T) {
	e := newEnv(t)
	e.typeText("/model pcc")
	e.enter()
	e.waitFor(t, 3*time.Second, func(s snapshot) bool { return s.model == "pcc" }, "model switch")
	e.quit()
}

func TestModelAskUserFlow(t *testing.T) {
	tool := `{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"ask_user","arguments":"{\"question\":\"which name?\"}"}}]}`
	e := newEnv(t, tool, `{"role":"assistant","content":"thanks"}`)
	e.typeText("go")
	e.enter()
	s := e.waitFor(t, 5*time.Second, func(s snapshot) bool { return s.ask }, "ask request")
	if !strings.Contains(s.view, "which name?") {
		t.Errorf("question not visible in view:\n%s", s.view)
	}
	e.typeText("myname")
	e.enter()
	s = e.waitFor(t, 5*time.Second, func(s snapshot) bool { return !s.running }, "run finished")
	if strings.Contains(s.view, "which name?") {
		t.Errorf("question bar must disappear after answering:\n%s", s.view)
	}
	found := false
	for _, b := range s.blocks {
		if b.kind == blockUser && strings.Contains(b.rendered, "myname") {
			found = true
		}
	}
	if !found {
		t.Error("answer not shown as user block")
	}
	e.quit()
}

func TestModelEscInterruptsRun(t *testing.T) {
	slow := `{"role":"assistant","content":"long answer"}`
	e := newEnv(t, slow)
	e.typeText("hi")
	e.enter()
	e.waitFor(t, 3*time.Second, func(s snapshot) bool { return s.running }, "running")
	e.prog.Send(tea.KeyMsg{Type: tea.KeyEsc})
	e.waitFor(t, 3*time.Second, func(s snapshot) bool { return !s.running }, "interrupted")
	e.quit()
}

var ansiStrip = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestModelToolBlocks(t *testing.T) {
	tool := `{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"list_files","arguments":"{\"pattern\":\"*\"}"}}]}`
	e := newEnv(t, tool, `{"role":"assistant","content":"final answer after tool"}`)
	e.typeText("list")
	e.enter()
	s := e.waitFor(t, 5*time.Second, func(s snapshot) bool { return !s.running }, "run finished")
	var sawTool, sawAnswer bool
	for _, b := range s.blocks {
		if b.kind == blockTool && !b.pend && b.name == "list_files" {
			sawTool = true
		}
		if b.kind == blockAssistant && strings.Contains(ansiStrip.ReplaceAllString(b.rendered, ""), "final answer after tool") {
			sawAnswer = true
		}
	}
	if !sawTool {
		t.Error("completed tool block missing")
	}
	if !sawAnswer {
		t.Error("answer after tool call missing (dropped deltas)")
	}
	e.quit()
}

func TestModelResizeReflows(t *testing.T) {
	e := newEnv(t, `{"role":"assistant","content":"some reasonably long answer that will wrap differently at different widths"}`)
	e.typeText("hi")
	e.enter()
	e.waitFor(t, 5*time.Second, func(s snapshot) bool {
		for _, b := range s.blocks {
			if b.kind == blockAssistant && b.rendered != "" {
				return true
			}
		}
		return false
	}, "assistant answer")
	before := e.snap()
	var renderedBefore string
	for _, b := range before.blocks {
		if b.kind == blockAssistant {
			renderedBefore = b.rendered
		}
	}

	e.prog.Send(tea.WindowSizeMsg{Width: 40, Height: 20})
	s := e.waitFor(t, 3*time.Second, func(s snapshot) bool {
		for _, b := range s.blocks {
			if b.kind == blockAssistant && b.rendered != "" && b.rendered != renderedBefore {
				return true
			}
		}
		return false
	}, "reflowed render at new width")
	_ = s
	e.quit()
}

func TestModelViewRenders(t *testing.T) {
	e := newEnv(t)
	s := e.snap()
	if !strings.Contains(s.view, "ready") || !strings.Contains(s.view, "❯") {
		t.Errorf("view missing elements:\n%s", s.view)
	}
	e.quit()
}
