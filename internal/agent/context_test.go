package agent

import (
	"context"
	"strings"
	"testing"

	"fm-agent/internal/serve"
)

func TestEstimateTokensGrowsWithHistory(t *testing.T) {
	srv, _ := newScriptedServer(t, `{"role":"assistant","content":"ok"}`)
	var out discardWriter
	a, _ := newTestAgentOpts(t, srv, &out, Options{})
	before := a.EstimateTokens()
	if before == 0 {
		t.Fatal("estimate must include system prompt and tool overhead")
	}
	a.Run(context.Background(), "please answer with a reasonably long message so the estimate grows")
	after := a.EstimateTokens()
	if after <= before {
		t.Errorf("before=%d after=%d", before, after)
	}
}

func TestCompactCollapsesOldMessages(t *testing.T) {
	summary := `{"role":"assistant","content":"SUMMARY: user asked for files, agent listed them."}`
	srv, fs := newScriptedServer(t, summary)
	var out discardWriter
	a, _ := newTestAgentOpts(t, srv, &out, Options{})

	// Build a history longer than compactionKeep.
	for i := 0; i < 5; i++ {
		a.history = append(a.history,
			serve.Message{Role: "user", Content: strings.Repeat("message ", 20)},
			serve.Message{Role: "assistant", Content: strings.Repeat("reply ", 20)},
		)
	}
	originalLen := len(a.history)

	if err := a.Compact(context.Background()); err != nil {
		t.Fatal(err)
	}
	hist := a.History()
	if len(hist) != 1+1+compactionKeep {
		t.Fatalf("history len = %d, want %d", len(hist), 1+1+compactionKeep)
	}
	if !strings.Contains(hist[1].Content, "<conversation_summary>") {
		t.Errorf("summary marker missing: %.100s", hist[1].Content)
	}
	if !strings.Contains(hist[1].Content, "SUMMARY") {
		t.Errorf("model summary missing: %.100s", hist[1].Content)
	}
	if len(a.History()) >= originalLen {
		t.Errorf("compaction did not shrink history: %d -> %d", originalLen, len(a.History()))
	}

	// The compaction request must not carry tools.
	fs.mu.Lock()
	req := fs.requests[len(fs.requests)-1]
	fs.mu.Unlock()
	if len(req.Tools) != 0 {
		t.Error("compaction request must be tool-less")
	}
	if req.Stream {
		t.Error("compaction request must be non-streaming")
	}
}

func TestCompactNothingToDo(t *testing.T) {
	srv, _ := newScriptedServer(t)
	var out discardWriter
	a, _ := newTestAgentOpts(t, srv, &out, Options{})
	if err := a.Compact(context.Background()); err == nil {
		t.Error("expected error when history is too short")
	}
}

func TestAutoCompactionDuringRun(t *testing.T) {
	summary := `{"role":"assistant","content":"SUMMARY"}`
	srv, fs := newScriptedServer(t, summary)
	var out discardWriter
	a, _ := newTestAgentOpts(t, srv, &out, Options{WindowTokens: 900})

	// Pre-fill history beyond the tiny window.
	for i := 0; i < 6; i++ {
		a.history = append(a.history,
			serve.Message{Role: "user", Content: strings.Repeat("long user content here ", 30)},
			serve.Message{Role: "assistant", Content: strings.Repeat("long assistant reply here ", 30)},
		)
	}
	if err := a.Run(context.Background(), "one more question"); err != nil {
		t.Fatal(err)
	}
	// Request 1: compaction (summary). Request 2: the actual answer.
	if fs.requestCount() != 2 {
		t.Fatalf("requests = %d, want 2", fs.requestCount())
	}
	if !strings.Contains(a.History()[1].Content, "SUMMARY") {
		t.Errorf("auto-compaction did not insert summary")
	}
}

func TestSetWindowTokens(t *testing.T) {
	srv, _ := newScriptedServer(t)
	var out discardWriter
	a, _ := newTestAgentOpts(t, srv, &out, Options{})
	if a.WindowTokens() != DefaultWindowTokens {
		t.Errorf("window = %d", a.WindowTokens())
	}
	a.SetWindowTokens(8192)
	if a.WindowTokens() != 8192 {
		t.Errorf("window = %d", a.WindowTokens())
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }
