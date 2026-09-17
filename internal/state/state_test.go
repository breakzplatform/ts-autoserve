package state

import (
	"os"
	"path/filepath"
	"testing"
)

func TestRoundTrip(t *testing.T) {
	s := New(filepath.Join(t.TempDir(), "sub", "state.json"))

	got, err := s.Load()
	if err != nil {
		t.Fatalf("Load on a first run: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("Load = %v, want empty on a first run", got)
	}

	if err := s.Save([]int{5173, 3000}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err = s.Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if !got[3000] || !got[5173] || len(got) != 2 {
		t.Fatalf("Load = %v, want 3000 and 5173", got)
	}

	// Saving an empty set is how shutdown says "nothing of mine is left".
	if err := s.Save(nil); err != nil {
		t.Fatalf("Save empty: %v", err)
	}
	if got, err = s.Load(); err != nil || len(got) != 0 {
		t.Fatalf("Load = %v, %v, want empty", got, err)
	}
}

func TestSaveLeavesNoTempFilesBehind(t *testing.T) {
	dir := t.TempDir()
	s := New(filepath.Join(dir, "state.json"))
	if err := s.Save([]int{3000}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "state.json" {
		t.Errorf("directory holds %v, want only state.json", entries)
	}
}

func TestLoadRejectsAGarbledFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	// An unreadable state file must be an error, never an empty set: the
	// daemon treats "nothing is mine" as "withdraw nothing".
	if _, err := New(path).Load(); err == nil {
		t.Error("Load accepted a garbled file")
	}
}
