package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"fm-agent/internal/safety"
	"fm-agent/internal/serve"
	"fm-agent/internal/tools"
)

type fakePrompter struct{}

func (fakePrompter) Confirm(string) bool { return true }
func (fakePrompter) Ask(q string) string { return "ok" }

// scriptedServer replays canned chat responses and records requests.
type scriptedServer struct {
	t *testing.T

	mu        sync.Mutex
	requests  []serve.ChatRequest
	responses []string
}

func newScriptedServer(t *testing.T, responses ...string) (*serve.Server, *scriptedServer) {
	t.Helper()
	fs := &scriptedServer{t: t, responses: responses}
	// Keep the socket path short: macOS sun_path limit is ~104 bytes and
	// t.TempDir() paths can exceed it once the test name is included.
	tmp, err := os.MkdirTemp("", "fma")
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
	mux.HandleFunc("/v1/chat/completions", fs.handle)
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return serve.NewServer("fm", sock), fs
}

func (fs *scriptedServer) handle(w http.ResponseWriter, r *http.Request) {
	var req serve.ChatRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	fs.mu.Lock()
	fs.requests = append(fs.requests, req)
	idx := len(fs.requests) - 1
	fs.mu.Unlock()

	w.Header().Set("Content-Type", "application/json")
	var msg serve.Message
	if idx >= len(fs.responses) {
		msg = serve.Message{Role: "assistant", Content: "done"}
	} else {
		canned := fs.responses[idx]
		// "ERROR:<code>:<message>" replays an API error instead of a message.
		if rest, ok := strings.CutPrefix(canned, "ERROR:"); ok {
			code, message, _ := strings.Cut(rest, ":")
			w.WriteHeader(http.StatusBadRequest)
			errBody, _ := json.Marshal(map[string]any{
				"error": map[string]any{
					"message": message,
					"type":    "invalid_request_error",
					"code":    code,
				},
			})
			w.Write(errBody)
			return
		}
		if err := json.Unmarshal([]byte(canned), &msg); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
	}
	usage := map[string]any{"prompt_tokens": 100, "completion_tokens": 10, "total_tokens": 110}
	if req.Stream {
		delta := map[string]any{"role": "assistant"}
		if msg.Content != "" {
			delta["content"] = msg.Content
		}
		if len(msg.ToolCalls) > 0 {
			delta["tool_calls"] = msg.ToolCalls
		}
		chunk, _ := json.Marshal(map[string]any{
			"choices": []any{map[string]any{"delta": delta, "finish_reason": "stop"}},
			"usage":   usage,
		})
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprintf(w, "data: %s\n\ndata: [DONE]\n\n", chunk)
		return
	}
	out, _ := json.Marshal(map[string]any{
		"choices": []any{map[string]any{"message": msg, "finish_reason": "stop"}},
		"usage":   usage,
	})
	w.Write(out)
}

func (fs *scriptedServer) requestCount() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()
	return len(fs.requests)
}

func newTestAgent(t *testing.T, srv *serve.Server, out io.Writer) (*Agent, string) {
	t.Helper()
	dir := t.TempDir()
	ws, err := safety.NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws, fakePrompter{})
	a := New(srv, reg, ws.Roots(), Options{Model: "system", Out: out})
	return a, dir
}

func newTestAgentOpts(t *testing.T, srv *serve.Server, out io.Writer, opts Options) (*Agent, string) {
	t.Helper()
	if opts.Model == "" {
		opts.Model = "system"
	}
	opts.Out = out
	dir := t.TempDir()
	ws, err := safety.NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	reg := tools.NewRegistry(ws, fakePrompter{})
	return New(srv, reg, ws.Roots(), opts), dir
}

func TestRunExecutesToolCallsThenAnswers(t *testing.T) {
	toolResp := `{"role":"assistant","tool_calls":[{"id":"call_1","type":"function","function":{"name":"list_files","arguments":"{\"pattern\":\"**\"}"}}]}`
	srv, fs := newScriptedServer(t, toolResp)
	var out bytes.Buffer
	a, _ := newTestAgent(t, srv, &out)

	err := a.Run(context.Background(), "what files are here?")
	if err != nil {
		t.Fatal(err)
	}
	if fs.requestCount() != 2 {
		t.Fatalf("expected 2 model calls, got %d", fs.requestCount())
	}

	// Second request must carry the assistant tool_call and the tool result.
	fs.mu.Lock()
	second := fs.requests[1]
	fs.mu.Unlock()
	if len(second.Tools) == 0 {
		t.Error("tools must be sent on every request")
	}
	var roles []string
	var sawToolResult bool
	for _, m := range second.Messages {
		roles = append(roles, m.Role)
		if m.Role == "tool" && m.ToolCallID == "call_1" {
			sawToolResult = true
			if !strings.Contains(m.Content, "untrusted") {
				t.Error("tool result must be marked untrusted")
			}
		}
	}
	if !sawToolResult {
		t.Errorf("no tool result message in %v", roles)
	}

	hist := a.History()
	if hist[0].Role != "system" {
		t.Error("history must start with system message")
	}
	last := hist[len(hist)-1]
	if last.Role != "assistant" || last.Content != "done" {
		t.Errorf("last message = %+v", last)
	}
	if u := a.LastUsage(); u == nil || u.TotalTokens != 110 {
		t.Errorf("usage = %+v", u)
	}
}

func TestRunStopsAtMaxIterations(t *testing.T) {
	toolA := `{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"list_files","arguments":"{\"pattern\":\"a\"}"}}]}`
	toolB := `{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"list_files","arguments":"{\"pattern\":\"b\"}"}}]}`
	// Alternating different calls: the loop must stop at maxIter even though
	// the model never produces a final answer.
	srv, _ := newScriptedServer(t, toolA, toolB, toolA, toolB, toolA)
	var out bytes.Buffer
	a, _ := newTestAgent(t, srv, &out)
	a.maxIter = 3

	err := a.Run(context.Background(), "loop forever")
	if err != ErrMaxIterations {
		t.Fatalf("err = %v", err)
	}
}

func TestRunStopsOnRepeatedToolCall(t *testing.T) {
	tool := `{"role":"assistant","tool_calls":[{"id":"c","type":"function","function":{"name":"list_files","arguments":"{\"pattern\":\"*\"}"}}]}`
	srv, _ := newScriptedServer(t, tool, tool, tool)
	var out bytes.Buffer
	a, _ := newTestAgent(t, srv, &out)

	err := a.Run(context.Background(), "loop forever")
	if err != ErrRepeatedToolCall {
		t.Fatalf("err = %v", err)
	}
}

func TestClearAndSetHistory(t *testing.T) {
	srv, _ := newScriptedServer(t)
	var out bytes.Buffer
	a, _ := newTestAgent(t, srv, &out)
	a.Run(context.Background(), "hi")
	if len(a.History()) < 3 {
		t.Fatalf("history too short: %d", len(a.History()))
	}
	a.Clear()
	if len(a.History()) != 1 || a.History()[0].Role != "system" {
		t.Errorf("after Clear: %v", a.History())
	}
	a.SetHistory([]serve.Message{{Role: "user", Content: "restored"}})
	if got := a.History()[1].Content; got != "restored" {
		t.Errorf("SetHistory: %q", got)
	}
}

func TestSetModel(t *testing.T) {
	srv, _ := newScriptedServer(t)
	var out bytes.Buffer
	a, _ := newTestAgent(t, srv, &out)
	if a.Model() != "system" {
		t.Errorf("default model = %q", a.Model())
	}
	a.SetModel("pcc")
	if a.Model() != "pcc" {
		t.Errorf("model = %q", a.Model())
	}
}
