package settings

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDefaultUntilSet(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s, err := Open(path, Settings{WritesEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if !s.WritesEnabled() {
		t.Fatal("default should apply while no file exists")
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("Open must not create the file: %v", err)
	}

	if err := s.Set(Settings{WritesEnabled: false}); err != nil {
		t.Fatal(err)
	}
	if s.WritesEnabled() {
		t.Fatal("Set did not apply")
	}

	// The stored value wins over the default on the next start.
	s2, err := Open(path, Settings{WritesEnabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if s2.WritesEnabled() {
		t.Fatal("stored value should override the default")
	}
}

func TestSetKeepsOldValueOnWriteFailure(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing-dir", "settings.json")
	s, err := Open(path, Settings{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Set(Settings{WritesEnabled: true}); err == nil {
		t.Fatal("expected an error for an unwritable path")
	}
	if s.WritesEnabled() {
		t.Fatal("failed Set must not change the live value")
	}
}

func TestCorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte("{"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(path, Settings{}); err == nil {
		t.Fatal("expected an error for a corrupt file")
	}
}

func TestNilStoreIsReadOnly(t *testing.T) {
	var s *Store
	if s.WritesEnabled() {
		t.Fatal("nil store must report writes disabled")
	}
}
