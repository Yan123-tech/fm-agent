package tui

import (
	"strings"
	"testing"
)

func collect(t *testing.T, input string) []Key {
	t.Helper()
	keys := ParseKeys(strings.NewReader(input))
	var out []Key
	for k := range keys {
		out = append(out, k)
	}
	return out
}

func TestParseRunesAndEnter(t *testing.T) {
	ks := collect(t, "hi\r")
	if len(ks) != 3 {
		t.Fatalf("got %v", ks)
	}
	if ks[0].Type != KeyRune || ks[0].R != 'h' {
		t.Errorf("ks[0] = %v", ks[0])
	}
	if ks[2].Type != KeyEnter {
		t.Errorf("ks[2] = %v", ks[2])
	}
}

func TestParseUTF8Rune(t *testing.T) {
	ks := collect(t, "Привет")
	if len(ks) != 6 {
		t.Fatalf("got %d keys", len(ks))
	}
	if ks[0].R != 'П' {
		t.Errorf("R = %c", ks[0].R)
	}
}

func TestParseArrowsAndNav(t *testing.T) {
	ks := collect(t, "\x1b[A\x1b[B\x1b[C\x1b[D\x1b[H\x1b[F\x1b[3~\x1b[5~\x1b[6~")
	want := []KeyType{KeyUp, KeyDown, KeyRight, KeyLeft, KeyHome, KeyEnd, KeyDelete, KeyPgUp, KeyPgDn}
	if len(ks) != len(want) {
		t.Fatalf("got %v", ks)
	}
	for i, w := range want {
		if ks[i].Type != w {
			t.Errorf("ks[%d] = %v, want %v", i, ks[i].Type, w)
		}
	}
}

func TestParseCtrlKeys(t *testing.T) {
	ks := collect(t, "\x01\x05\x15\x0b\x17\x03\x04\x0a\x7f")
	want := []KeyType{KeyHome, KeyEnd, keyClearInput, keyKillLine, keyDeleteWord, KeyCtrlC, KeyCtrlD, KeyEnter, KeyBackspace}
	if len(ks) != len(want) {
		t.Fatalf("got %v", ks)
	}
	for i, w := range want {
		if ks[i].Type != w {
			t.Errorf("ks[%d] = %v, want %v", i, ks[i].Type, w)
		}
	}
}

func TestParseEscAlone(t *testing.T) {
	ks := collect(t, "\x1b")
	if len(ks) != 1 || ks[0].Type != KeyEsc {
		t.Fatalf("got %v", ks)
	}
}

func TestParseAltEnter(t *testing.T) {
	ks := collect(t, "\x1b\r")
	if len(ks) != 1 || ks[0].Type != KeyAltEnter {
		t.Fatalf("got %v", ks)
	}
}

func TestParsePaste(t *testing.T) {
	ks := collect(t, "\x1b[200~pasted text\x1b[201~")
	var got string
	for _, k := range ks {
		if k.Type != KeyRune {
			t.Fatalf("unexpected key %v", k)
		}
		got += string(k.R)
	}
	if got != "pasted text" {
		t.Errorf("got %q", got)
	}
}
