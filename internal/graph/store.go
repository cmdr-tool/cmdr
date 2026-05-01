package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
)

// Store manages the on-disk layout of graph data:
//
//	~/.cmdr/graphs/<slug>/
//	    snapshots/<sha>/graph.json
//	    snapshots/<sha>/report.md
//	    cache/<content-sha256>.json
//	    .graph_root
//
// Snapshots are immutable. The cache is content-hashed, so unchanged
// files skip re-extraction even across snapshots.
type Store struct {
	root string
}

// NewStore returns a Store rooted at ~/.cmdr/graphs.
func NewStore() (*Store, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("graph: user home: %w", err)
	}
	root := filepath.Join(home, ".cmdr", "graphs")
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("graph: mkdir root: %w", err)
	}
	return &Store{root: root}, nil
}

// NewStoreAt returns a Store rooted at an explicit directory; used by tests.
func NewStoreAt(root string) (*Store, error) {
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("graph: mkdir root: %w", err)
	}
	return &Store{root: root}, nil
}

// Root returns the absolute path to the store root.
func (s *Store) Root() string { return s.root }

// RepoDir returns the per-repo storage directory, creating it if needed.
// Also writes a .graph_root marker pointing back at the source repo.
func (s *Store) RepoDir(slug, repoPath string) (string, error) {
	dir := filepath.Join(s.root, slug)
	if err := os.MkdirAll(filepath.Join(dir, "snapshots"), 0o700); err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Join(dir, "cache"), 0o700); err != nil {
		return "", err
	}
	marker := filepath.Join(dir, ".graph_root")
	if _, err := os.Stat(marker); os.IsNotExist(err) {
		_ = os.WriteFile(marker, []byte(repoPath+"\n"), 0o600)
	}
	return dir, nil
}

// SnapshotDir is the directory holding a single immutable snapshot.
func (s *Store) SnapshotDir(slug, sha string) string {
	return filepath.Join(s.root, slug, "snapshots", sha)
}

// HasSnapshot reports whether a graph.json already exists for sha.
func (s *Store) HasSnapshot(slug, sha string) bool {
	_, err := os.Stat(filepath.Join(s.SnapshotDir(slug, sha), "graph.json"))
	return err == nil
}

// WriteSnapshot serializes snap to <slug>/snapshots/<sha>/graph.json.
func (s *Store) WriteSnapshot(slug, sha string, snap *Snapshot) error {
	dir := s.SnapshotDir(slug, sha)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("graph: mkdir snapshot: %w", err)
	}
	path := filepath.Join(dir, "graph.json")
	data, err := json.MarshalIndent(snap, "", "  ")
	if err != nil {
		return fmt.Errorf("graph: marshal: %w", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return fmt.Errorf("graph: write snapshot: %w", err)
	}
	return nil
}

// WriteReport writes the human-readable report.md alongside graph.json.
func (s *Store) WriteReport(slug, sha string, body []byte) error {
	dir := s.SnapshotDir(slug, sha)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(dir, "report.md"), body, 0o600)
}

// ReadSnapshot loads a snapshot from disk.
func (s *Store) ReadSnapshot(slug, sha string) (*Snapshot, error) {
	path := filepath.Join(s.SnapshotDir(slug, sha), "graph.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var snap Snapshot
	if err := json.Unmarshal(data, &snap); err != nil {
		return nil, fmt.Errorf("graph: unmarshal: %w", err)
	}
	return &snap, nil
}

// TracesPath returns the absolute path to traces.json for a snapshot.
// Existence is not implied — see HasTraces.
func (s *Store) TracesPath(slug, sha string) string {
	return filepath.Join(s.SnapshotDir(slug, sha), "traces.json")
}

// HasTraces reports whether traces.json exists for a snapshot.
func (s *Store) HasTraces(slug, sha string) bool {
	_, err := os.Stat(s.TracesPath(slug, sha))
	return err == nil
}

// WriteTraces writes raw bytes to traces.json. Caller is responsible for
// JSON marshaling — keeps this method free of dependencies on graphtrace.
func (s *Store) WriteTraces(slug, sha string, data []byte) error {
	dir := s.SnapshotDir(slug, sha)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("graph: mkdir snapshot: %w", err)
	}
	return os.WriteFile(s.TracesPath(slug, sha), data, 0o600)
}

// ReadTraces returns the raw contents of traces.json.
func (s *Store) ReadTraces(slug, sha string) ([]byte, error) {
	return os.ReadFile(s.TracesPath(slug, sha))
}

// CacheKey hashes content for the per-file extraction cache.
func CacheKey(content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:])
}

// CachePath returns the cache file path for a content hash.
func (s *Store) CachePath(slug, key string) string {
	return filepath.Join(s.root, slug, "cache", key+".json")
}

// LoadCachedExtraction returns a previously-cached extraction, if any.
func (s *Store) LoadCachedExtraction(slug, key string) (*FileExtraction, bool) {
	data, err := os.ReadFile(s.CachePath(slug, key))
	if err != nil {
		return nil, false
	}
	var fx FileExtraction
	if err := json.Unmarshal(data, &fx); err != nil {
		return nil, false
	}
	return &fx, true
}

// SaveCachedExtraction persists fx under the content-hash key.
func (s *Store) SaveCachedExtraction(slug, key string, fx *FileExtraction) error {
	data, err := json.Marshal(fx)
	if err != nil {
		return err
	}
	return os.WriteFile(s.CachePath(slug, key), data, 0o600)
}
