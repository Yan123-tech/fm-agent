package serve

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeFM runs an HTTP handler on a Unix socket and returns the socket path.
func fakeFM(t *testing.T, handler http.HandlerFunc) string {
	t.Helper()
	// Keep the socket path short: macOS sun_path limit is ~104 bytes and
	// t.TempDir() paths can exceed it once the test name is included.
	tmp, err := os.MkdirTemp("", "fmsrv")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(tmp) })
	sock := filepath.Join(tmp, "s.sock")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: handler}
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() {
		_ = srv.Close()
	})
	return sock
}

func TestChatNonStream(t *testing.T) {
	sock := fakeFM(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":10,"completion_tokens":3,"total_tokens":13}}`)
	})
	s := NewServer("fm", sock)
	resp, err := s.Chat(context.Background(), &ChatRequest{Model: "system", Messages: []Message{{Role: "user", Content: "hi"}}})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "hello" {
		t.Errorf("content = %q", resp.Message.Content)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 13 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestChatStreamAssemblesToolCalls(t *testing.T) {
	sock := fakeFM(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		chunks := []string{
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"call_1","type":"function","function":{"name":"read_file","arguments":"{\"path\""}}]}}]}`,
			`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":":\"main.go\"}"}}]}}]}`,
			`{"choices":[{"delta":{"content":"Done."},"finish_reason":"stop"}],"usage":{"prompt_tokens":20,"completion_tokens":5,"total_tokens":25}}`,
			"[DONE]",
		}
		for _, c := range chunks {
			fmt.Fprintf(w, "data: %s\n\n", c)
		}
	})
	s := NewServer("fm", sock)
	var streamed strings.Builder
	resp, err := s.ChatStream(context.Background(), &ChatRequest{Model: "system"}, func(d string) {
		streamed.WriteString(d)
	})
	if err != nil {
		t.Fatal(err)
	}
	if streamed.String() != "Done." {
		t.Errorf("streamed = %q", streamed.String())
	}
	if len(resp.Message.ToolCalls) != 1 {
		t.Fatalf("tool calls = %+v", resp.Message.ToolCalls)
	}
	tc := resp.Message.ToolCalls[0]
	if tc.ID != "call_1" || tc.Function.Name != "read_file" || tc.Function.Arguments != `{"path":"main.go"}` {
		t.Errorf("tool call = %+v", tc)
	}
	if resp.Usage == nil || resp.Usage.TotalTokens != 25 {
		t.Errorf("usage = %+v", resp.Usage)
	}
}

func TestAPIErrorRetryable(t *testing.T) {
	retryable := []string{"model_service_unavailable", "rate_limit_exceeded", "upstream_closed"}
	for _, code := range retryable {
		e := &APIError{Code: code, Message: "x"}
		if !e.Retryable() {
			t.Errorf("%s should be retryable", code)
		}
	}
	fatal := []string{"content_filter", "context_length_exceeded", "invalid_request"}
	for _, code := range fatal {
		e := &APIError{Code: code, Message: "x"}
		if e.Retryable() {
			t.Errorf("%s should not be retryable", code)
		}
	}
}

func TestChatRetriesOnTransientError(t *testing.T) {
	calls := 0
	sock := fakeFM(t, func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, `{"error":{"message":"busy","type":"server_error","code":"model_service_unavailable"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"ok"},"finish_reason":"stop"}]}`)
	})
	// Shrink backoff for the test.
	retryBase = time.Millisecond
	retryCap = 2 * time.Millisecond
	t.Cleanup(func() { retryBase, retryCap = time.Second, 8*time.Second })

	s := NewServer("fm", sock)
	resp, err := s.Chat(context.Background(), &ChatRequest{Model: "system"})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Message.Content != "ok" || calls != 3 {
		t.Errorf("content=%q calls=%d", resp.Message.Content, calls)
	}
}

func TestChatDoesNotRetryFatalError(t *testing.T) {
	sock := fakeFM(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error":{"message":"too big","type":"invalid_request_error","code":"context_length_exceeded"}}`)
	})
	s := NewServer("fm", sock)
	_, err := s.Chat(context.Background(), &ChatRequest{Model: "system"})
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected APIError, got %v", err)
	}
	if apiErr.Retryable() {
		t.Error("context_length_exceeded must not be retried")
	}
}

// fm serve returns an SSE stream when "stream" is absent from the request;
// a non-streaming client must send stream:false explicitly and still parse.
func TestChatNonStreamAgainstSSEDefaultServer(t *testing.T) {
	sock := fakeFM(t, func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if stream, ok := body["stream"].(bool); ok && !stream {
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"choices":[{"message":{"role":"assistant","content":"plain json"},"finish_reason":"stop"}]}`)
			return
		}
		// No explicit stream:false -> this server (like fm serve) streams.
		w.Header().Set("Content-Type", "text/event-stream")
		fmt.Fprint(w, "data: {\"choices\":[{\"message\":{\"role\":\"assistant\",\"content\":\"sse\"},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n")
	})
	s := NewServer("fm", sock)
	resp, err := s.Chat(context.Background(), &ChatRequest{Model: "system"})
	if err != nil {
		t.Fatalf("Chat failed: %v", err)
	}
	if resp.Message.Content != "plain json" {
		t.Errorf("content = %q", resp.Message.Content)
	}
}
