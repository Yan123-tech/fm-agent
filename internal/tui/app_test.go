package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"fm-agent/internal/agent"
	"fm-agent/internal/safety"
	"fm-agent/internal/serve"
	"fm-agent/internal/session"
	"fm-agent/internal/tools"
)

// stubServe replays canned messages (one per request) over a UDS HTTP server.
func stubServe(t *testing.T, canned ...string) *serve.Server {
	t.Helper()
	tmp, err := os.MkdirTemp("", "fmapptui")
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
	return serve.NewServer("fm", sock)
}

type testEnv struct {
	app   *App
	ag    *agent.Agent
	keys  chan Key
	out   *syncBuf
	dir   string
	store *session.Store
}

type syncBuf struct {
	mu  sync.Mutex
	buf strings.Builder
}

func (b *syncBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}
func (b *syncBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func newTestApp(t *testing.T, canned ...string) *testEnv {
	t.Helper()
	srv := stubServe(t, canned...)
	dir := t.TempDir()
	ws, err := safety.NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws, safety.AutoApprove{})
	out := &syncBuf{}
	ag := agent.New(srv, reg, ws.Roots(), agent.Options{Out: out})
	store, err := session.NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	keys := make(chan Key, 128)
	app := NewApp(ag, store, out, keys, "test")
	app.SizeFn = func() (int, int) { return 80, 24 }
	ag.SetOut(app.StreamWriter())
	ag.SetToolPrinter(app.ToolPrinter())
	ag.SetToolEventPrinter(app.ToolEventPrinter())
	reg.SetPrompter(app.Prompter())
	return &testEnv{app: app, ag: ag, keys: keys, out: out, dir: dir, store: store}
}

// typeKeys sends a string as rune keys followed by Enter.
func (e *testEnv) typeKeys(s string) {
	for _, r := range s {
		e.keys <- Key{Type: KeyRune, R: r}
	}
	e.keys <- Key{Type: KeyEnter}
}

// waitFor polls cond until true or timeout.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timeout waiting for %s", what)
}

func TestAppRunsTurnAndExits(t *testing.T) {
	env := newTestApp(t, `{"role":"assistant","content":"**bold** answer"}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- env.app.Run(ctx) }()

	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(env.out.String(), "/help for commands")
	}, "header")

	env.typeKeys("hi")
	waitFor(t, 3*time.Second, func() bool {
		env.app.mu.Lock()
		defer env.app.mu.Unlock()
		if len(env.app.blocks) == 0 {
			return false
		}
		last := env.app.blocks[len(env.app.blocks)-1]
		return last.kind == blockAssistant && last.final
	}, "assistant final block")

	env.typeKeys("/exit")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("app did not exit")
	}

	out := env.out.String()
	if !strings.Contains(out, "\x1b[?1049h") {
		t.Error("alternate screen not entered")
	}
	if !strings.Contains(out, ansiBold+"bold"+ansiReset) {
		t.Error("markdown answer not rendered")
	}
	if !strings.Contains(out, "❯") {
		t.Error("input prompt missing")
	}
}

func TestAppSlashCommand(t *testing.T) {
	env := newTestApp(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- env.app.Run(ctx) }()
	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(env.out.String(), "fm-agent")
	}, "header")

	env.typeKeys("/model pcc")
	waitFor(t, 2*time.Second, func() bool { return env.ag.Model() == "pcc" }, "model switch")
	if !strings.Contains(env.out.String(), "model set to pcc") {
		t.Errorf("command output missing:\n%s", env.out.String())
	}
	env.typeKeys("/exit")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("app did not exit")
	}
}

func TestAppConfirmationKeypress(t *testing.T) {
	tool := `{"role":"assistant","tool_calls":[{"id":"c1","type":"function","function":{"name":"write_file","arguments":"{\"path\":\"ok.txt\",\"content\":\"yes\"}"}}]}`
	env := newTestApp(t, tool, `{"role":"assistant","content":"written"}`)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- env.app.Run(ctx) }()
	waitFor(t, 2*time.Second, func() bool {
		return strings.Contains(env.out.String(), "fm-agent")
	}, "header")

	env.typeKeys("create a file")
	waitFor(t, 2*time.Second, func() bool {
		env.app.mu.Lock()
		defer env.app.mu.Unlock()
		return len(env.app.confirmQ) > 0
	}, "confirm request")

	env.keys <- Key{Type: KeyRune, R: 'y'}
	waitFor(t, 3*time.Second, func() bool {
		_, err := os.Stat(filepath.Join(env.dir, "ok.txt"))
		return err == nil
	}, "file created after y")

	waitFor(t, 2*time.Second, func() bool {
		env.app.mu.Lock()
		defer env.app.mu.Unlock()
		return !env.app.running
	}, "run finished")
	env.typeKeys("/exit")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("app did not exit")
	}
}

func TestAppInputEditing(t *testing.T) {
	env := newTestApp(t)
	env.app.handleKey(Key{Type: KeyRune, R: 'a'})
	env.app.handleKey(Key{Type: KeyRune, R: 'b'})
	env.app.handleKey(Key{Type: KeyRune, R: 'c'})
	env.app.handleKey(Key{Type: KeyLeft})
	env.app.handleKey(Key{Type: KeyBackspace})
	env.app.handleKey(Key{Type: KeyRune, R: 'X'})
	if got := string(env.app.input); got != "aXc" {
		t.Errorf("input = %q", got)
	}
	env.app.handleKey(Key{Type: keyClearInput})
	if len(env.app.input) != 0 {
		t.Errorf("ctrl+u left %q", string(env.app.input))
	}
}

func TestAppHistoryRecall(t *testing.T) {
	env := newTestApp(t)
	env.app.hist = []string{"first", "second"}
	env.app.handleKey(Key{Type: KeyUp})
	if string(env.app.input) != "second" {
		t.Errorf("input = %q", string(env.app.input))
	}
	env.app.handleKey(Key{Type: KeyUp})
	if string(env.app.input) != "first" {
		t.Errorf("input = %q", string(env.app.input))
	}
	env.app.handleKey(Key{Type: KeyDown})
	if string(env.app.input) != "second" {
		t.Errorf("input = %q", string(env.app.input))
	}
	env.app.handleKey(Key{Type: KeyDown})
	if len(env.app.input) != 0 {
		t.Errorf("input = %q", string(env.app.input))
	}
}
