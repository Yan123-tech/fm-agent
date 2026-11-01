package tui

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fm-agent/internal/agent"
	"fm-agent/internal/safety"
	"fm-agent/internal/serve"
	"fm-agent/internal/session"
	"fm-agent/internal/tools"
)

type autoYes struct{}

func (autoYes) Confirm(string) bool { return true }
func (autoYes) Ask(q string) string { return "yes" }

// stubServe spins up a canned fm serve double on a Unix socket.
func stubServe(t *testing.T, canned ...string) *serve.Server {
	t.Helper()
	tmp, err := os.MkdirTemp("", "fmtui")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	sock := filepath.Join(tmp, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	var count int
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req serve.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		msg := serve.Message{Role: "assistant", Content: "done"}
		if count < len(canned) {
			_ = json.Unmarshal([]byte(canned[count]), &msg)
		}
		count++
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
	return serve.NewServer("fm", sock)
}

func newTestTUI(t *testing.T, input string, canned ...string) (*TUI, *bytesBuffer, *agent.Agent) {
	t.Helper()
	srv := stubServe(t, canned...)
	dir := t.TempDir()
	ws, err := safety.NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws, autoYes{})
	out := &bytesBuffer{}
	ag := agent.New(srv, reg, ws.Roots(), agent.Options{Out: out})
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	ui := New(ag, store, bufio.NewReader(strings.NewReader(input)), out, "test")
	ag.SetToolPrinter(ui.ToolPrinter())
	return ui, out, ag
}

type bytesBuffer struct{ b strings.Builder }

func (w *bytesBuffer) Write(p []byte) (int, error) { return w.b.Write(p) }
func (w *bytesBuffer) String() string              { return w.b.String() }

func TestHeaderAndAnswerRendered(t *testing.T) {
	ui, out, _ := newTestTUI(t, "hi\n/exit\n", `{"role":"assistant","content":"**bold** answer"}`)
	if err := ui.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "fm-agent") || !strings.Contains(s, "window") {
		t.Errorf("header missing:\n%s", s)
	}
	if !strings.Contains(s, ansiBold+"bold"+ansiReset) {
		t.Errorf("markdown answer not rendered:\n%s", s)
	}
	if !strings.Contains(s, "est ") || !strings.Contains(s, "tokens") {
		t.Errorf("status line missing:\n%s", s)
	}
}

func TestToolLineStyled(t *testing.T) {
	tool := `{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"list_files","arguments":"{\"pattern\":\"*\"}"}}]}`
	ui, out, _ := newTestTUI(t, "list\n/exit\n", tool, `{"role":"assistant","content":"files listed"}`)
	if err := ui.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	s := out.String()
	if !strings.Contains(s, "⚒") || !strings.Contains(s, "list_files") {
		t.Errorf("tool line missing:\n%s", s)
	}
}

func TestSlashCommandShared(t *testing.T) {
	ui, out, ag := newTestTUI(t, "/model pcc\n/exit\n")
	if err := ui.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ag.Model() != "pcc" {
		t.Errorf("model = %q", ag.Model())
	}
	if !strings.Contains(out.String(), "model set to pcc") {
		t.Errorf("command output missing:\n%s", out.String())
	}
}

func TestErrorShownInRed(t *testing.T) {
	ui, out, _ := newTestTUI(t, "/bogus\n/exit\n")
	if err := ui.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), ansiRed+"error:") {
		t.Errorf("red error missing:\n%s", out.String())
	}
}
