package safety

import (
	"os"
	"path/filepath"
	"testing"
)

func newWS(t *testing.T) (*Workspace, string) {
	t.Helper()
	dir := t.TempDir()
	ws, err := NewWorkspace(dir)
	if err != nil {
		t.Fatal(err)
	}
	return ws, dir
}

func TestResolveRelative(t *testing.T) {
	ws, dir := newWS(t)
	wantRoot, _ := filepath.EvalSymlinks(dir)
	got, err := ws.Resolve("sub/file.go")
	if err != nil {
		t.Fatal(err)
	}
	want := filepath.Join(wantRoot, "sub", "file.go")
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestResolveEscapesWorkspace(t *testing.T) {
	ws, _ := newWS(t)
	for _, p := range []string{"../outside.txt", "../../etc/passwd", "sub/../../../etc/passwd"} {
		if _, err := ws.Resolve(p); err == nil {
			t.Errorf("%s should be rejected", p)
		}
	}
}

func TestResolveAbsoluteOutside(t *testing.T) {
	ws, _ := newWS(t)
	if _, err := ws.Resolve("/etc/passwd"); err == nil {
		t.Error("absolute path outside workspace should be rejected")
	}
}

func TestResolveSymlinkEscape(t *testing.T) {
	ws, dir := newWS(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := ws.Resolve("link"); err == nil {
		t.Error("symlink escaping the workspace should be rejected")
	}
}

func TestResolveInsideIsAllowed(t *testing.T) {
	ws, dir := newWS(t)
	target := filepath.Join(dir, "real.txt")
	if err := os.WriteFile(target, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "alias")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	got, err := ws.Resolve("alias")
	if err != nil {
		t.Fatalf("in-workspace symlink should be allowed: %v", err)
	}
	want, _ := filepath.EvalSymlinks(target)
	if got != want {
		t.Errorf("got %q, want resolved target %q", got, want)
	}
}
