// Package store locates what the tool has on this machine: one directory per
// cluster, holding the describe artifact and the run record. State is derived
// from the world where it can be -- see docs/design/03-assembly.md §1 -- and
// this package holds only what the world cannot tell us.
package store

import (
	"os"
	"path/filepath"
	"sort"
)

type Store struct{ Home string }

// Open resolves the store root: $CSB_HOME, else ~/.local/share/csb.
func Open() (*Store, error) {
	if h := os.Getenv("CSB_HOME"); h != "" {
		return &Store{Home: h}, nil
	}
	base, err := os.UserHomeDir()
	if err != nil {
		return nil, err
	}
	return &Store{Home: filepath.Join(base, ".local", "share", "csb")}, nil
}

func (s *Store) ClustersDir() string           { return filepath.Join(s.Home, "clusters") }
func (s *Store) ClusterDir(name string) string { return filepath.Join(s.ClustersDir(), name) }
func (s *Store) DescribePath(name string) string {
	return filepath.Join(s.ClusterDir(name), "describe.json")
}
func (s *Store) RecordPath(name string) string {
	return filepath.Join(s.ClusterDir(name), "record.jsonl")
}

// List returns the cluster names this machine has state for, sorted.
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.ClustersDir())
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var names []string
	for _, e := range entries {
		if e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	return names, nil
}

func (s *Store) Exists(name string) bool {
	fi, err := os.Stat(s.ClusterDir(name))
	return err == nil && fi.IsDir()
}

func (s *Store) EnsureCluster(name string) error {
	return os.MkdirAll(s.ClusterDir(name), 0o755)
}
