package tui

import (
	"strings"
	"testing"
)

func TestVisibleWidthIgnoresANSI(t *testing.T) {
	if got := visibleWidth(ansiBold + "hello" + ansiReset); got != 5 {
		t.Errorf("got %d", got)
	}
}

func TestWrapShortLineUnchanged(t *testing.T) {
	out := wrapAnsi("hello", 10)
	if len(out) != 1 || out[0] != "hello" {
		t.Errorf("got %v", out)
	}
}

func TestWrapAtSpace(t *testing.T) {
	out := wrapAnsi("aaa bbb ccc", 7)
	if len(out) != 2 {
		t.Fatalf("got %v", out)
	}
	if strings.Contains(out[0], "bbb") || !strings.Contains(out[1], "bbb") {
		t.Errorf("wrap point wrong: %v", out)
	}
}

func TestWrapPreservesStyleAcrossLines(t *testing.T) {
	line := ansiBold + "one two three four" + ansiReset
	out := wrapAnsi(line, 9)
	if len(out) < 2 {
		t.Fatalf("no wrap: %v", out)
	}
	for i, l := range out {
		if !strings.HasPrefix(l, ansiBold) {
			t.Errorf("line %d lost bold prefix: %q", i, l)
		}
		if !strings.HasSuffix(l, ansiReset) {
			t.Errorf("line %d lost reset suffix: %q", i, l)
		}
		if visibleWidth(l) > 9 {
			t.Errorf("line %d too wide: %d", i, visibleWidth(l))
		}
	}
}

func TestWrapHardBreaksLongWord(t *testing.T) {
	out := wrapAnsi("abcdefghij", 4)
	if len(out) != 3 {
		t.Fatalf("got %v", out)
	}
	for _, l := range out {
		if visibleWidth(l) > 4 {
			t.Errorf("line too wide: %q", l)
		}
	}
}
