package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"

	"fm-agent/internal/serve"
)

// DefaultWindowTokens is the context window of the on-device model on
// current devices (macOS 27 reports 8192 via fm chat). Older devices ship a
// 4096-token model; the agent detects overflow at runtime and shrinks
// automatically. Override with Options.WindowTokens or the --window flag.
const DefaultWindowTokens = 8192

// MinWindowTokens is the lowest the adaptive window will shrink to.
const MinWindowTokens = 2048

// toolsOverhead approximates the prompt tokens consumed by the tool specs
// sent on every request.
const toolsOverhead = 700

// compactionKeep is how many recent messages survive compaction.
const compactionKeep = 4

// WindowTokens returns the configured context window.
func (a *Agent) WindowTokens() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.windowTokens
}

// SetWindowTokens overrides the context window estimate.
func (a *Agent) SetWindowTokens(n int) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.windowTokens = n
}

// EstimateTokens heuristically estimates the tokens the next request will
// occupy: system prompt + transcript + tool specs. It deliberately
// overestimates (~3.3 chars/token) because under-filling the window is
// harmless while overflowing it fails the request. When the server reports
// usage, the measured prompt tokens of the last request are used as a floor.
func (a *Agent) EstimateTokens() int {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.estimateTokens()
}

// estimateTokens is the lock-free core; callers must hold a.mu.
func (a *Agent) estimateTokens() int {
	total := toolsOverhead
	for _, m := range a.history {
		total += 4 + estimateMessageTokens(m)
	}
	if a.measuredPrompt > total {
		total = a.measuredPrompt
	}
	return total
}

func estimateMessageTokens(m serve.Message) int {
	n := estimateStringTokens(m.Content)
	for _, tc := range m.ToolCalls {
		n += estimateStringTokens(tc.Function.Name) + estimateStringTokens(tc.Function.Arguments) + 8
	}
	return n
}

func estimateStringTokens(s string) int {
	if s == "" {
		return 0
	}
	runes := utf8.RuneCountInString(s)
	if runes/3 < 1 {
		return 1
	}
	return runes / 3
}

// Compact collapses old history into a model-generated summary, keeping the
// most recent messages intact. It is triggered automatically when the
// estimated prompt approaches the window, and manually via /compact.
func (a *Agent) Compact(ctx context.Context) error {
	a.mu.Lock()
	msgs := a.history[1:] // skip system
	if len(msgs) <= compactionKeep {
		a.mu.Unlock()
		return errors.New("nothing to compact yet")
	}
	old, recent := msgs[:len(msgs)-compactionKeep], msgs[len(msgs)-compactionKeep:]
	a.mu.Unlock()

	req := &serve.ChatRequest{
		Model: a.Model(),
		Messages: []serve.Message{
			{
				Role: "system",
				Content: "You compress conversation transcripts. Produce a dense factual summary under 200 words. " +
					"Preserve: decisions made, file paths read or edited, commands run, errors hit, and pending user requests. " +
					"Use the same language as the excerpt.",
			},
			{Role: "user", Content: "Summarize this conversation excerpt:\n\n" + renderExcerpt(old)},
		},
	}
	resp, err := a.srv.Chat(ctx, req)
	if err != nil {
		return err
	}
	summary := strings.TrimSpace(resp.Message.Content)
	if summary == "" {
		return errors.New("model returned an empty summary")
	}

	marker := fmt.Sprintf("<conversation_summary>\n%s\n</conversation_summary>\n"+
		"The above summary replaces earlier messages in this conversation. Treat it as your memory of what happened.", summary)
	a.mu.Lock()
	a.history = append([]serve.Message{a.history[0], {Role: "user", Content: marker}}, recent...)
	a.mu.Unlock()
	return nil
}

// renderExcerpt flattens messages into a compact text form, truncating long
// contents so the compaction request itself fits comfortably in the window.
func renderExcerpt(msgs []serve.Message) string {
	var b strings.Builder
	budget := 6000 // chars ≈ 1500–2000 tokens
	for _, m := range msgs {
		body := m.Content
		if len(body) > 500 {
			body = body[:500] + " …"
		}
		label := m.Role
		if m.Role == "tool" {
			label = "tool_result"
		}
		for _, tc := range m.ToolCalls {
			fmt.Fprintf(&b, "%s called tool %s(%.200s)\n", m.Role, tc.Function.Name, tc.Function.Arguments)
		}
		if body != "" {
			fmt.Fprintf(&b, "%s: %s\n", label, body)
		}
		if b.Len() > budget {
			b.WriteString("…[excerpt truncated]\n")
			break
		}
	}
	return b.String()
}
