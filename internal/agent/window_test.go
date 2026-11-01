package agent

import (
	"context"
	"errors"
	"strings"
	"testing"

	"fm-agent/internal/serve"
)

func TestAdaptiveShrinkOnOverflow(t *testing.T) {
	// 1) model call overflows; 2) compaction summary; 3) retried call succeeds.
	srv, fs := newScriptedServer(t,
		"ERROR:context_length_exceeded:prompt too large",
		`{"role":"assistant","content":"SUMMARY"}`,
		`{"role":"assistant","content":"recovered"}`,
	)
	var out discardWriter
	a, _ := newTestAgentOpts(t, srv, &out, Options{})

	// History long enough for Compact to have something to collapse,
	// but well under the 8192-token threshold so no auto-compaction fires.
	for i := 0; i < 5; i++ {
		a.history = append(a.history,
			serve.Message{Role: "user", Content: strings.Repeat("content ", 10)},
			serve.Message{Role: "assistant", Content: strings.Repeat("reply ", 10)},
		)
	}

	if err := a.Run(context.Background(), "try again please"); err != nil {
		t.Fatal(err)
	}
	if a.WindowTokens() != DefaultWindowTokens/2 {
		t.Errorf("window = %d, want %d", a.WindowTokens(), DefaultWindowTokens/2)
	}
	if fs.requestCount() != 3 {
		t.Errorf("requests = %d, want 3 (overflow, compact, retry)", fs.requestCount())
	}
	last := a.History()[len(a.History())-1]
	if last.Content != "recovered" {
		t.Errorf("final answer = %q", last.Content)
	}
}

func TestOverflowNotRetryableBelowFloor(t *testing.T) {
	srv, fs := newScriptedServer(t, "ERROR:context_length_exceeded:still too big")
	var out discardWriter
	a, _ := newTestAgentOpts(t, srv, &out, Options{})
	a.SetWindowTokens(MinWindowTokens)

	err := a.Run(context.Background(), "hi")
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *serve.APIError
	if !errors.As(err, &apiErr) || apiErr.Code != "context_length_exceeded" {
		t.Errorf("err = %v", err)
	}
	if fs.requestCount() != 1 {
		t.Errorf("requests = %d, want 1 (no retry at floor)", fs.requestCount())
	}
}

func TestMeasuredPromptTokensFloor(t *testing.T) {
	srv, _ := newScriptedServer(t, `{"role":"assistant","content":"ok"}`)
	var out discardWriter
	a, _ := newTestAgentOpts(t, srv, &out, Options{})
	if err := a.Run(context.Background(), "hi"); err != nil {
		t.Fatal(err)
	}
	// The stub reports prompt_tokens=100; the estimate must not drop below
	// the measured value even though the heuristic sees a tiny history.
	if got := a.EstimateTokens(); got < 100 {
		t.Errorf("EstimateTokens() = %d, want >= 100", got)
	}
}
