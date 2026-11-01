package repl

import (
	"bufio"
	"bytes"
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

// stubServer always answers with a fixed message.
func stubServer(t *testing.T, answer string) *serve.Server {
	t.Helper()
	tmp, err := os.MkdirTemp("", "fmr")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	sock := filepath.Join(tmp, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/chat/completions", func(w http.ResponseWriter, r *http.Request) {
		var req serve.ChatRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		delta := map[string]any{"role": "assistant", "content": answer}
		chunk, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"delta": delta, "finish_reason": "stop"}},
			"usage":   map[string]any{"prompt_tokens": 1, "completion_tokens": 1, "total_tokens": 2},
		})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return serve.NewServer("fm", sock)
}

func newTestREPL(t *testing.T, input string) (*REPL, *agent.Agent, *session.Store, *bytes.Buffer) {
	t.Helper()
	srv := stubServer(t, "ok")
	dir := t.TempDir()
	ws, err := safety.NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws, autoYes{})
	out := &bytes.Buffer{}
	ag := agent.New(srv, reg, ws.Roots(), agent.Options{Out: out})
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	r := New(ag, store, bufio.NewReader(strings.NewReader(input)), out, Options{Interactive: false})
	return r, ag, store, out
}

func TestRunAnswersAndExits(t *testing.T) {
	r, ag, store, out := newTestREPL(t, "hello\n/exit\n")
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ok") {
		t.Errorf("missing model answer:\n%s", out.String())
	}
	if len(ag.History()) != 3 { // system + user + assistant
		t.Errorf("history = %d messages", len(ag.History()))
	}
	if _, err := store.Load(DefaultSessionName); err != nil {
		t.Errorf("session not autosaved: %v", err)
	}
}

func TestEofEndsLoop(t *testing.T) {
	r, _, _, out := newTestREPL(t, "hi")
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ok") {
		t.Errorf("missing answer:\n%s", out.String())
	}
}

func TestModelCommand(t *testing.T) {
	r, ag, _, out := newTestREPL(t, "/model pcc\n/exit\n")
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ag.Model() != "pcc" {
		t.Errorf("model = %q", ag.Model())
	}
	if !strings.Contains(out.String(), "model set to pcc") {
		t.Errorf("missing confirmation:\n%s", out.String())
	}
}

func TestClearCommand(t *testing.T) {
	r, ag, _, _ := newTestREPL(t, "hi\n/clear\n/exit\n")
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(ag.History()) != 1 {
		t.Errorf("history after /clear = %d", len(ag.History()))
	}
}

func TestUnknownCommand(t *testing.T) {
	r, _, _, out := newTestREPL(t, "/bogus\n/exit\n")
	if err := r.Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "unknown command") {
		t.Errorf("missing error:\n%s", out.String())
	}
}
