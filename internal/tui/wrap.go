package tui

import (
	"strings"
	"unicode/utf8"
)

// visibleWidth returns the number of terminal cells occupied by s once
// ANSI SGR sequences are removed (runes are assumed width 1).
func visibleWidth(s string) int {
	n := 0
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			// skip CSI sequence
			j := i + 1
			if j < len(s) && s[j] == '[' {
				j++
				for j < len(s) && (s[j] < 0x40 || s[j] > 0x7e) {
					j++
				}
				j++
			}
			i = j
			continue
		}
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
		n++
	}
	return n
}

// wrapAnsi soft-wraps a possibly-styled line to the given cell width,
// breaking at spaces when possible. Active SGR state is re-applied after
// each wrap and reset at the end of every emitted line.
func wrapAnsi(line string, width int) []string {
	if width <= 0 || visibleWidth(line) <= width {
		return []string{line}
	}
	var out []string
	var seg strings.Builder
	var state strings.Builder
	col := 0
	lastSpace := -1 // byte offset of last space in seg

	flush := func() {
		s := seg.String()
		out = append(out, s+ansiReset)
		seg.Reset()
		col = 0
		lastSpace = -1
	}
	i := 0
	for i < len(line) {
		if line[i] == 0x1b { // copy escape sequence through, update state
			j := i + 1
			if j < len(line) && line[j] == '[' {
				j++
				for j < len(line) && (line[j] < 0x40 || line[j] > 0x7e) {
					j++
				}
				j++
			}
			seq := line[i:j]
			seg.WriteString(seq)
			updateSGRState(&state, seq)
			i = j
			continue
		}
		r, size := utf8.DecodeRuneInString(line[i:])
		if col >= width {
			if lastSpace > 0 {
				// wrap at the last space: emit up to it, keep the rest
				s := seg.String()
				head, tail := s[:lastSpace], s[lastSpace+1:]
				out = append(out, head+ansiReset)
				seg.Reset()
				seg.WriteString(sgrPrefix(&state))
				seg.WriteString(tail)
				col = visibleWidth(tail)
			} else {
				flush()
				seg.WriteString(sgrPrefix(&state))
			}
			lastSpace = -1
		}
		seg.WriteRune(r)
		if r == ' ' {
			lastSpace = seg.Len() - 1
		}
		col++
		i += size
	}
	flush()
	return out
}

// updateSGRState maintains the active SGR sequences so they can be
// re-emitted after a wrap; "0" clears everything.
func updateSGRState(state *strings.Builder, seq string) {
	if !strings.HasPrefix(seq, "\x1b[") || !strings.HasSuffix(seq, "m") {
		return
	}
	body := seq[2 : len(seq)-1]
	if body == "0" || body == "" {
		state.Reset()
		return
	}
	for _, code := range strings.Split(body, ";") {
		if code == "0" {
			state.Reset()
			continue
		}
		if state.Len() > 0 {
			state.WriteString(";")
		}
		state.WriteString(code)
	}
}

// sgrPrefix returns the re-emittable escape sequence for an accumulated
// state body, or "" when no style is active.
func sgrPrefix(state *strings.Builder) string {
	if state.Len() == 0 {
		return ""
	}
	return "\x1b[" + state.String() + "m"
}
