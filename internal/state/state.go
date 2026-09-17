// Package state remembers which ports this daemon published, across restarts.
//
// ipn.ServeConfig records no author, so at startup the daemon cannot tell its
// own leftovers from a `tailscale serve` the user set up to live there. Asking
// "is something listening on it?" is not enough: a mapping the user keeps for
// a server that happens to be stopped -- after a reboot, before the dev server
// is started again -- looks exactly like litter from a run that died.
//
// So the daemon writes down what it published, and cleans up nothing else.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
)

// Store is the file the daemon keeps its published ports in.
type Store struct{ Path string }

// New returns a store backed by path.
func New(path string) *Store { return &Store{Path: path} }

// DefaultPath is where the state file lives. It goes under the state
// directory rather than next to config.yaml: nobody edits this by hand, and it
// is not worth syncing between machines.
func DefaultPath() string {
	if dir := os.Getenv("XDG_STATE_HOME"); dir != "" {
		return filepath.Join(dir, "ts-autoserve", "state.json")
	}
	if home, err := os.UserHomeDir(); err == nil {
		return filepath.Join(home, ".local", "state", "ts-autoserve", "state.json")
	}
	return "ts-autoserve-state.json"
}

type file struct {
	Ports []int `json:"ports"`
}

// Load reports the ports the last run had published. A missing file is a first
// run, not a failure.
func (s *Store) Load() (map[int]bool, error) {
	data, err := os.ReadFile(s.Path)
	if os.IsNotExist(err) {
		return map[int]bool{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", s.Path, err)
	}
	var f file
	if err := json.Unmarshal(data, &f); err != nil {
		return nil, fmt.Errorf("%s: %w", s.Path, err)
	}
	out := make(map[int]bool, len(f.Ports))
	for _, p := range f.Ports {
		out[p] = true
	}
	return out, nil
}

// Save replaces the file with this set of ports.
//
// The write goes through a temporary file and a rename, because the daemon is
// usually killed rather than stopped: a half-written state file would leave the
// next run unable to tell its own mappings from the user's.
func (s *Store) Save(ports []int) error {
	sort.Ints(ports)
	data, err := json.Marshal(file{Ports: ports})
	if err != nil {
		return err
	}
	dir := filepath.Dir(s.Path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create %s: %w", dir, err)
	}
	tmp, err := os.CreateTemp(dir, ".state-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name()) // no-op once the rename succeeded
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), s.Path)
}
