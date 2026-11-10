// Package settings holds the settings an Admin can change at runtime from
// the dashboard. They are kept in a small JSON file in the data directory,
// so a choice made in the UI survives restarts and image updates.
package settings

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
)

// Settings is the persisted state. New fields need a sensible zero value or
// handling in Open, because older files will not have them.
type Settings struct {
	WritesEnabled bool `json:"writes_enabled"`
}

// Store serves the current settings. Reads are lock-free because the MCP
// handler checks WritesEnabled on every request.
type Store struct {
	path   string
	mu     sync.Mutex // serialises Set, so the file and memory agree
	writes atomic.Bool
}

// Open loads the settings from path. When the file does not exist yet, def
// applies until the first Set; that is how MCP_ENABLE_WRITES acts as the
// initial value without overriding what an Admin chose later.
func Open(path string, def Settings) (*Store, error) {
	s := &Store{path: path}
	cur := def
	b, err := os.ReadFile(path)
	switch {
	case errors.Is(err, fs.ErrNotExist):
	case err != nil:
		return nil, fmt.Errorf("settings: %w", err)
	default:
		if err := json.Unmarshal(b, &cur); err != nil {
			return nil, fmt.Errorf("settings: %s: %w", path, err)
		}
	}
	s.writes.Store(cur.WritesEnabled)
	return s, nil
}

// Memory returns a store that is never written to disk, for tests.
func Memory(v Settings) *Store {
	s := &Store{}
	s.writes.Store(v.WritesEnabled)
	return s
}

// Get returns the current settings.
func (s *Store) Get() Settings {
	return Settings{WritesEnabled: s.WritesEnabled()}
}

// WritesEnabled reports whether write tools are served. A nil store reports
// false, so forgetting to wire settings fails closed.
func (s *Store) WritesEnabled() bool {
	return s != nil && s.writes.Load()
}

// Set persists v and then applies it. If the file cannot be written the
// old settings stay in effect, so the UI never shows a state that would
// be lost on restart.
func (s *Store) Set(v Settings) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.path != "" {
		if err := writeFileAtomic(s.path, v); err != nil {
			return fmt.Errorf("settings: %w", err)
		}
	}
	s.writes.Store(v.WritesEnabled)
	return nil
}

func writeFileAtomic(path string, v Settings) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op after a successful rename
	if _, err := tmp.Write(append(b, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
