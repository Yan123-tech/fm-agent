package tui

import (
	"bufio"
	"io"
	"unicode/utf8"
)

// KeyType classifies parsed key events.
type KeyType int

const (
	KeyRune KeyType = iota
	KeyEnter
	KeyEsc
	KeyCtrlC
	KeyCtrlD
	KeyAltEnter // newline insertion
	KeyUp
	KeyDown
	KeyLeft
	KeyRight
	KeyPgUp
	KeyPgDn
	KeyHome
	KeyEnd
	KeyDelete
	KeyBackspace
	keyKillLine   // Ctrl-K: delete from cursor to end of line
	keyClearInput // Ctrl-U: delete whole input
	keyDeleteWord // Ctrl-W: delete word before cursor
)

// Key is a single input event.
type Key struct {
	Type KeyType
	R    rune
}

// ParseKeys reads raw bytes and emits decoded key events. It runs until the
// reader fails or closes; the channel is closed at the end.
func ParseKeys(r io.Reader) <-chan Key {
	out := make(chan Key, 64)
	go func() {
		defer close(out)
		br := bufio.NewReader(r)
		for {
			b, err := br.ReadByte()
			if err != nil {
				return
			}
			for _, k := range decodeKey(br, b) {
				out <- k
			}
		}
	}()
	return out
}

// decodeKey interprets one leading byte, consuming more from br when the
// sequence requires it. It returns zero or more keys (bracketed paste can
// yield many runes).
func decodeKey(br *bufio.Reader, b byte) []Key {
	switch {
	case b == 0x1b:
		return decodeEscape(br)
	case b == '\r' || b == '\n':
		// Enter (and Ctrl+J, which terminals send as the same byte)
		return []Key{{Type: KeyEnter}}
	case b == 0x03:
		return []Key{{Type: KeyCtrlC}}
	case b == 0x04:
		return []Key{{Type: KeyCtrlD}}
	case b == 0x7f || b == 0x08:
		return []Key{{Type: KeyBackspace}}
	case b < 0x20:
		// remaining control keys
		switch b {
		case 0x01: // Ctrl-A
			return []Key{{Type: KeyHome}}
		case 0x02: // Ctrl-B
			return []Key{{Type: KeyLeft}}
		case 0x05: // Ctrl-E
			return []Key{{Type: KeyEnd}}
		case 0x06: // Ctrl-F
			return []Key{{Type: KeyRight}}
		case 0x0b: // Ctrl-K
			return []Key{{Type: keyKillLine}}
		case 0x0e: // Ctrl-N
			return []Key{{Type: KeyDown}}
		case 0x10: // Ctrl-P
			return []Key{{Type: KeyUp}}
		case 0x15: // Ctrl-U
			return []Key{{Type: keyClearInput}}
		case 0x17: // Ctrl-W
			return []Key{{Type: keyDeleteWord}}
		}
		return nil
	default:
		// UTF-8 rune (possibly multi-byte)
		size := 1
		switch {
		case b&0xE0 == 0xC0:
			size = 2
		case b&0xF0 == 0xE0:
			size = 3
		case b&0xF8 == 0xF0:
			size = 4
		}
		buf := []byte{b}
		for i := 1; i < size; i++ {
			nb, err := br.ReadByte()
			if err != nil {
				return nil
			}
			buf = append(buf, nb)
		}
		r, _ := utf8.DecodeRune(buf)
		if r == utf8.RuneError {
			return nil
		}
		return []Key{{Type: KeyRune, R: r}}
	}
}

func decodeEscape(br *bufio.Reader) []Key {
	b, err := br.ReadByte()
	if err != nil {
		return []Key{{Type: KeyEsc}}
	}
	switch b {
	case '[':
		return decodeCSI(br)
	case 'O':
		// SS3: F-keys etc.; consume one more byte and ignore
		_, _ = br.ReadByte()
		return nil
	case '\r':
		return []Key{{Type: KeyAltEnter}}
	default:
		// Alt+<rune>: treat as plain rune (no alt-modifier handling needed)
		return decodeKey(br, b)
	}
}

func decodeCSI(br *bufio.Reader) []Key {
	b, err := br.ReadByte()
	if err != nil {
		return []Key{{Type: KeyEsc}}
	}
	// Bracketed paste?
	if b == '2' {
		rest, _ := br.Peek(2)
		if string(rest) == "00" {
			// consume "00~" of the 200~ start marker
			_, _ = br.ReadByte()
			_, _ = br.ReadByte()
			_, _ = br.ReadByte()
			return readPaste(br)
		}
	}
	// Numeric parameter sequences like "5~" or "1;5D".
	if b >= '0' && b <= '9' {
		var param [8]byte
		n := 0
		for b >= '0' && b <= '9' && n < len(param) {
			param[n] = b
			n++
			b, err = br.ReadByte()
			if err != nil {
				return nil
			}
		}
		if b != '~' {
			// modifier+letter forms (e.g. 1;5D): consume to final byte
			for !isCSIFinal(b) {
				b, err = br.ReadByte()
				if err != nil {
					return nil
				}
			}
		}
		switch string(param[:n]) {
		case "1", "7":
			return []Key{{Type: KeyHome}}
		case "4", "8":
			return []Key{{Type: KeyEnd}}
		case "3":
			return []Key{{Type: KeyDelete}}
		case "5":
			return []Key{{Type: KeyPgUp}}
		case "6":
			return []Key{{Type: KeyPgDn}}
		}
		return nil
	}
	switch b {
	case 'A':
		return []Key{{Type: KeyUp}}
	case 'B':
		return []Key{{Type: KeyDown}}
	case 'C':
		return []Key{{Type: KeyRight}}
	case 'D':
		return []Key{{Type: KeyLeft}}
	case 'H':
		return []Key{{Type: KeyHome}}
	case 'F':
		return []Key{{Type: KeyEnd}}
	case 'Z':
		return nil // shift-tab unused
	}
	return nil
}

func isCSIFinal(b byte) bool { return b >= 0x40 && b <= 0x7e }

// readPaste consumes bytes until the paste-end sequence ESC [ 201 ~ and
// emits the pasted content as runes.
func readPaste(br *bufio.Reader) []Key {
	var buf []byte
	for {
		b, err := br.ReadByte()
		if err != nil {
			break
		}
		buf = append(buf, b)
		if len(buf) >= 6 && string(buf[len(buf)-6:]) == "\x1b[201~" {
			buf = buf[:len(buf)-6]
			break
		}
	}
	var keys []Key
	for len(buf) > 0 {
		r, size := utf8.DecodeRune(buf)
		if r == utf8.RuneError && size <= 1 {
			buf = buf[1:]
			continue
		}
		keys = append(keys, Key{Type: KeyRune, R: r})
		buf = buf[size:]
	}
	return keys
}
