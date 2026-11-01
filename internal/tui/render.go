package tui

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// render draws one full frame: transcript body, divider, input box,
// optional confirm/ask bar, status line, then positions the cursor.
// Every row of the screen is rewritten each frame, so no residue remains.
func (app *App) render() {
	app.mu.Lock()
	defer app.mu.Unlock()

	w, h := app.SizeFn()
	if w < 24 || h < 8 {
		return
	}

	// --- transcript, wrapped to width ---
	var lines []string
	for _, b := range app.blocks {
		lines = append(lines, b.view()...)
	}
	var wrapped []string
	for _, l := range lines {
		wrapped = append(wrapped, wrapAnsi(l, w)...)
	}

	// --- input box ---
	inputLines := app.renderInput(w)

	var extra []string // confirm / ask bars
	if len(app.confirmQ) > 0 {
		extra = append(extra, ansiYellow+"  ⚠ allow? "+app.confirmQ[0].desc+" [y/n]"+ansiReset)
	}
	if len(app.askQ) > 0 {
		extra = append(extra, ansiYellow+"  ❓ "+app.askQ[0].question+" — type the answer below, Enter to send"+ansiReset)
	}
	status := app.statusLine(w)

	fixed := 1 + len(inputLines) + len(extra) + 1 // divider + input + bars + status
	bodyH := h - fixed
	if bodyH < 1 {
		bodyH = 1
	}

	total := len(wrapped)
	maxScroll := total - bodyH
	if maxScroll < 0 {
		maxScroll = 0
	}
	if app.scroll > maxScroll {
		app.scroll = maxScroll
	}
	end := total - app.scroll
	start := end - bodyH
	if start < 0 {
		start = 0
	}
	vis := wrapped[start:end]

	var buf strings.Builder
	buf.WriteString("\x1b[H")
	for _, l := range vis {
		buf.WriteString(l)
		buf.WriteString("\r\n")
	}
	for i := len(vis); i < bodyH; i++ {
		buf.WriteString("\r\n")
	}
	buf.WriteString(ansiDim + strings.Repeat("─", w) + ansiReset + "\r\n")
	for _, l := range inputLines {
		buf.WriteString(l)
		buf.WriteString("\x1b[K\r\n")
	}
	for _, e := range extra {
		buf.WriteString(e)
		buf.WriteString("\x1b[K\r\n")
	}
	buf.WriteString(status)
	buf.WriteString("\x1b[K")
	buf.WriteString(app.cursorSeq(h, fixed, len(inputLines)))
	fmt.Fprint(app.out, buf.String())
}

// renderInput returns the input box lines with prompt prefixes.
func (app *App) renderInput(w int) []string {
	var out []string
	for i, l := range strings.Split(string(app.input), "\n") {
		prefix := ansiBold + ansiGreen + "❯" + ansiReset + " "
		if i > 0 {
			prefix = "  "
		}
		out = append(out, truncateVis(prefix+l, w))
	}
	if len(out) == 0 {
		out = []string{ansiBold + ansiGreen + "❯" + ansiReset + " "}
	}
	return out
}

// statusLine is the bottom bar: activity, model, context usage, hints.
func (app *App) statusLine(w int) string {
	var left strings.Builder
	switch {
	case len(app.confirmQ) > 0:
		left.WriteString(ansiYellow + "⚠ awaiting confirmation" + ansiReset + "  ")
	case app.running:
		left.WriteString(ansiCyan + spinnerFrames[app.spinner%len(spinnerFrames)] + " thinking" + ansiReset + "  ")
	default:
		left.WriteString(ansiGreen + "● ready" + ansiReset + "  ")
	}
	left.WriteString(ansiDim + app.ag.Model())
	fmt.Fprintf(&left, " · est %d/%d tok", app.ag.EstimateTokens(), app.ag.WindowTokens())
	fmt.Fprintf(&left, " · %s", app.sessionName)
	left.WriteString(ansiReset)

	right := ansiDim + "alt+enter newline · esc interrupt · ctrl+c quit" + ansiReset
	lw := visibleWidth(left.String())
	rw := visibleWidth(right)
	gap := w - lw - rw
	if gap < 1 {
		gap = 1
	}
	line := left.String() + strings.Repeat(" ", gap) + right
	if visibleWidth(line) > w {
		line = truncateVis(line, w)
	}
	return line
}

// cursorSeq positions the terminal cursor inside the input box.
func (app *App) cursorSeq(h, fixed, inputRows int) string {
	// rows from top: body (h-fixed) + divider (1), then input lines
	inputTop := h - fixed + 2
	lineIdx := 0
	colInLine := app.cursor
	for i := 0; i < app.cursor; i++ {
		if app.input[i] == '\n' {
			lineIdx++
			colInLine = app.cursor - i - 1
		}
	}
	row := inputTop + lineIdx
	if row > h-1 {
		row = h - 1
	}
	col := colInLine + 3 // "> " prefix is 2 cells, terminals are 1-based
	return fmt.Sprintf("\x1b[%d;%dH", row, col)
}

// truncateVis clips s to at most w visible cells.
func truncateVis(s string, w int) string {
	if visibleWidth(s) <= w {
		return s
	}
	var b strings.Builder
	col := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
					j++
				}
				j++
			}
			b.WriteString(s[i:j])
			i = j
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		if col >= w-1 {
			break
		}
		b.WriteString(s[i : i+size])
		col++
		i += size
	}
	b.WriteString(ansiReset)
	return b.String()
}
