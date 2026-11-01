// Package tui implements the full-screen terminal interface: raw-mode
// alternate screen, real-time streaming, multiline input, scrollback and
// keypress confirmations — Claude Code style. Standard library only.
package tui

import (
	"strings"
)

// ANSI SGR fragments used across the UI.
const (
	ansiReset    = "\x1b[0m"
	ansiBold     = "\x1b[1m"
	ansiDim      = "\x1b[2m"
	ansiItalic   = "\x1b[3m"
	ansiRed      = "\x1b[31m"
	ansiGreen    = "\x1b[32m"
	ansiYellow   = "\x1b[33m"
	ansiCyan     = "\x1b[36m"
	ansiGray     = "\x1b[90m"
	ansiClear    = "\x1b[2J\x1b[H"
	ansiEraseEOW = "\x1b[J" // erase from cursor to end of screen
)

// mdStream renders markdown incrementally, line by line, so streaming
// output can be displayed as it arrives.
type mdStream struct {
	inFence bool
}

// feedLine renders one complete line and updates fence state. It returns
// "" for fence delimiter lines (```).
func (m *mdStream) feedLine(line string) string {
	trimmed := strings.TrimSpace(line)
	switch {
	case strings.HasPrefix(trimmed, "```"):
		m.inFence = !m.inFence
		return ""
	case m.inFence:
		return ansiGray + "  │ " + line + ansiReset
	case strings.HasPrefix(line, "### "):
		return ansiBold + ansiCyan + line[4:] + ansiReset
	case strings.HasPrefix(line, "## "):
		return ansiBold + ansiCyan + line[3:] + ansiReset
	case strings.HasPrefix(line, "# "):
		return ansiBold + ansiCyan + line[2:] + ansiReset
	default:
		return renderInline(line)
	}
}

// partial renders the not-yet-complete tail line.
func (m *mdStream) partial(line string) string {
	if line == "" {
		return ""
	}
	if m.inFence {
		return ansiGray + "  │ " + line + ansiReset
	}
	return renderInline(line)
}

// RenderMarkdown converts a markdown-lite subset to styled terminal text:
// fenced code blocks, headers, bold, inline code, italics. Unknown markup
// passes through verbatim.
func RenderMarkdown(src string) string {
	var out strings.Builder
	ms := &mdStream{}
	for _, line := range strings.Split(src, "\n") {
		if r := ms.feedLine(line); r != "" {
			out.WriteString(r + "\n")
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

// renderInline styles **bold**, *italic* and `code` spans within a line.
func renderInline(line string) string {
	var out strings.Builder
	for len(line) > 0 {
		boldIdx := strings.Index(line, "**")
		codeIdx := strings.Index(line, "`")
		italIdx := strings.Index(line, "*")

		// nearest opening marker
		next := -1
		kind := byte(0)
		for _, c := range []struct {
			i int
			k byte
		}{{boldIdx, '*'}, {codeIdx, '`'}, {italIdx, '*'}} {
			if c.i >= 0 && (next < 0 || c.i < next) {
				next, kind = c.i, c.k
			}
		}
		if next < 0 {
			out.WriteString(line)
			break
		}
		// write text before the marker
		out.WriteString(line[:next])
		marker := string(kind)
		if kind == '*' && strings.HasPrefix(line[next:], "**") {
			marker = "**"
		}
		rest := line[next+len(marker):]
		end := strings.Index(rest, marker)
		if end < 0 {
			// unbalanced: emit marker verbatim
			out.WriteString(marker)
			line = rest
			continue
		}
		inner := rest[:end]
		switch marker {
		case "**":
			out.WriteString(ansiBold + inner + ansiReset)
		case "`":
			out.WriteString(ansiYellow + inner + ansiReset)
		default:
			out.WriteString(ansiItalic + inner + ansiReset)
		}
		line = rest[end+len(marker):]
	}
	return out.String()
}
