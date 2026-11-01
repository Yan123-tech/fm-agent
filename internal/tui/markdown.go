// Package tui implements the chat-style terminal interface: ANSI colors,
// a waiting spinner, markdown-lite rendering and a per-turn status bar.
// It uses no external dependencies; plain line discipline, terminal
// scrollback handles history.
package tui

import (
	"strings"
)

// ANSI SGR fragments used across the UI.
const (
	ansiReset  = "\x1b[0m"
	ansiBold   = "\x1b[1m"
	ansiDim    = "\x1b[2m"
	ansiItalic = "\x1b[3m"
	ansiRed    = "\x1b[31m"
	ansiGreen  = "\x1b[32m"
	ansiYellow = "\x1b[33m"
	ansiCyan   = "\x1b[36m"
	ansiGray   = "\x1b[90m"
)

// RenderMarkdown converts a markdown-lite subset to styled terminal text:
// fenced code blocks, headers, bold, inline code, italics. Unknown markup
// passes through verbatim.
func RenderMarkdown(src string) string {
	var out strings.Builder
	inFence := false
	for _, line := range strings.Split(src, "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```"):
			inFence = !inFence
			continue
		case inFence:
			out.WriteString(ansiGray + "  │ " + line + ansiReset + "\n")
			continue
		case strings.HasPrefix(line, "### "):
			out.WriteString(ansiBold + ansiCyan + line[4:] + ansiReset + "\n")
		case strings.HasPrefix(line, "## "):
			out.WriteString(ansiBold + ansiCyan + line[3:] + ansiReset + "\n")
		case strings.HasPrefix(line, "# "):
			out.WriteString(ansiBold + ansiCyan + line[2:] + ansiReset + "\n")
		case trimmed == "":
			out.WriteString("\n")
		default:
			out.WriteString(renderInline(line) + "\n")
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
