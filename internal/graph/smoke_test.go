//go:build smoke

package graph

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// TestSmoke_SelfBuild runs the full pipeline against the cmdr repo
// itself and prints a summary. Gated by the "smoke" build tag so it
// doesn't run in normal `go test` runs.
//
//	go test -tags=smoke -run TestSmoke_SelfBuild -count=1 -v ./internal/graph
func TestSmoke_SelfBuild(t *testing.T) {
	repo, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(repo, "go.mod")); err != nil {
		t.Skipf("not running from a checkout: %v", err)
	}

	store, err := NewStoreAt(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	slug := Slug(repo)
	store.RepoDir(slug, repo)

	snap, err := Build(BuildOptions{
		RepoPath:  repo,
		CommitSHA: "smoke",
		Slug:      slug,
		Store:     store,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	t.Logf("nodes=%d edges=%d communities=%d languages=%v",
		snap.Stats.NodeCount, snap.Stats.EdgeCount, snap.Stats.CommunityCount, snap.Snapshot.Languages)

	pretty, _ := json.MarshalIndent(snap.Stats, "", "  ")
	t.Logf("stats:\n%s", pretty)

	if snap.Stats.NodeCount < 50 {
		t.Errorf("expected at least 50 nodes for cmdr repo, got %d", snap.Stats.NodeCount)
	}
}
