// Package session persists conversation transcripts as JSONL files under
// ~/.fm-agent/sessions.
package session

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"

	"fm-agent/internal/serve"
)

// NamePattern restricts session names to filesystem-safe tokens.
var NamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]*$`)

// Store saves and loads JSONL transcripts.
type Store struct {
	dir string
}

// Dir returns the store directory.
func (s *Store) Dir() string { return s.dir }

// NewStore opens the default store under the user's home directory.
func NewStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	dir := filepath.Join(home, ".fm-agent", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// NewStoreAt opens a store at an explicit directory (mainly for tests).
func NewStoreAt(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	return &Store{dir: dir}, nil
}

// Path returns the JSONL path for a session name.
func (s *Store) Path(name string) string {
	return filepath.Join(s.dir, name+".jsonl")
}

// Valid reports whether name is usable.
func Valid(name string) bool { return NamePattern.MatchString(name) }

// Save writes the transcript as JSONL, one message per line.
func (s *Store) Save(name string, msgs []serve.Message) error {
	if !Valid(name) {
		return fmt.Errorf("invalid session name %q (allowed: letters, digits, -, _)", name)
	}
	f, err := os.Create(s.Path(name))
	if err != nil {
		return err
	}
	defer f.Close()
	w := bufio.NewWriter(f)
	for _, m := range msgs {
		b, err := json.Marshal(m)
		if err != nil {
			return err
		}
		if _, err := w.WriteString(string(b) + "\n"); err != nil {
			return err
		}
	}
	return w.Flush()
}

// Load reads a transcript saved by Save.
func (s *Store) Load(name string) ([]serve.Message, error) {
	if !Valid(name) {
		return nil, fmt.Errorf("invalid session name %q", name)
	}
	f, err := os.Open(s.Path(name))
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var msgs []serve.Message
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		var m serve.Message
		if err := json.Unmarshal(line, &m); err != nil {
			return nil, fmt.Errorf("%s: %w", s.Path(name), err)
		}
		msgs = append(msgs, m)
	}
	return msgs, sc.Err()
}
