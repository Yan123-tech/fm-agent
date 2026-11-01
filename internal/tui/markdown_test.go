package tui

import (
	"strings"
	"testing"
)

func TestRenderCodeFence(t *testing.T) {
	src := "Here is code:\n```go\nfmt.Println(\"hi\")\n```\nDone."
	out := RenderMarkdown(src)
	if !strings.Contains(out, ansiGray+"  │ fmt.Println(\"hi\")"+ansiReset) {
		t.Errorf("code line not styled:\n%q", out)
	}
	if strings.Contains(out, "```") {
		t.Errorf("fences must be consumed:\n%q", out)
	}
	if !strings.HasSuffix(out, "Done.") {
		t.Errorf("trailing text lost:\n%q", out)
	}
}

func TestRenderBoldAndInlineCode(t *testing.T) {
	out := RenderMarkdown("use **read_file** and `offset`")
	if !strings.Contains(out, ansiBold+"read_file"+ansiReset) {
		t.Errorf("bold missing:\n%q", out)
	}
	if !strings.Contains(out, ansiYellow+"offset"+ansiReset) {
		t.Errorf("inline code missing:\n%q", out)
	}
}

func TestRenderHeaders(t *testing.T) {
	out := RenderMarkdown("# Title\n## Sub\nbody")
	if !strings.Contains(out, ansiBold+ansiCyan+"Title"+ansiReset) {
		t.Errorf("h1 missing:\n%q", out)
	}
	if !strings.Contains(out, ansiBold+ansiCyan+"Sub"+ansiReset) {
		t.Errorf("h2 missing:\n%q", out)
	}
	if !strings.Contains(out, "body") {
		t.Errorf("body missing:\n%q", out)
	}
}

func TestRenderUnbalancedMarkers(t *testing.T) {
	out := RenderMarkdown("a **b and `c")
	if !strings.Contains(out, "**b") {
		t.Errorf("unbalanced bold marker should pass through:\n%q", out)
	}
	if !strings.Contains(out, "`c") {
		t.Errorf("unbalanced code marker should pass through:\n%q", out)
	}
}

func TestRenderItalic(t *testing.T) {
	out := RenderMarkdown("some *emphasis* here")
	if !strings.Contains(out, ansiItalic+"emphasis"+ansiReset) {
		t.Errorf("italic missing:\n%q", out)
	}
}

func TestRenderTrimsTrailingNewline(t *testing.T) {
	if out := RenderMarkdown("line\n\n"); strings.HasSuffix(out, "\n") {
		t.Errorf("trailing newline kept: %q", out)
	}
}
