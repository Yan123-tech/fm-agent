package session

import (
	"os"
	"path/filepath"
	"testing"

	"fm-agent/internal/serve"
)

func TestSaveLoadRoundTrip(t *testing.T) {
	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	msgs := []serve.Message{
		{Role: "system", Content: "sys"},
		{Role: "user", Content: "hi"},
		{Role: "assistant", Content: "hello", ToolCalls: []serve.ToolCall{{
			ID:   "call_1",
			Type: "function",
		}}},
		{Role: "tool", ToolCallID: "call_1", Content: "result"},
	}
	if err := store.Save("demo", msgs); err != nil {
		t.Fatal(err)
	}
	got, err := store.Load("demo")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(msgs) {
		t.Fatalf("got %d messages, want %d", len(got), len(msgs))
	}
	for i := range msgs {
		if got[i].Role != msgs[i].Role || got[i].Content != msgs[i].Content {
			t.Errorf("msg %d = %+v, want %+v", i, got[i], msgs[i])
		}
	}
	if got[2].ToolCalls[0].ID != "call_1" {
		t.Errorf("tool call lost: %+v", got[2].ToolCalls)
	}
}

func TestInvalidNames(t *testing.T) {
	store, _ := NewStoreAt(t.TempDir())
	for _, bad := range []string{"../evil", "a/b", "no spaces", ".hidden", ""} {
		if Valid(bad) {
			t.Errorf("%q must be invalid", bad)
		}
		if err := store.Save(bad, nil); err == nil {
			t.Errorf("Save(%q) must fail", bad)
		}
	}
	for _, good := range []string{"demo", "my-session_2", "A1"} {
		if !Valid(good) {
			t.Errorf("%q must be valid", good)
		}
	}
}

func TestLoadMissing(t *testing.T) {
	store, _ := NewStoreAt(t.TempDir())
	if _, err := store.Load("nope"); !os.IsNotExist(err) {
		t.Errorf("err = %v", err)
	}
}

func TestPathStaysInDir(t *testing.T) {
	dir := t.TempDir()
	store, _ := NewStoreAt(dir)
	if filepath.Dir(store.Path("x")) != dir {
		t.Errorf("path escaped: %s", store.Path("x"))
	}
}
