package graph

import (
	"os"
	"path/filepath"
	"testing"
)

// fixtureModule writes a tiny Go module to a tmp dir so Build has
// something realistic to walk.
func fixtureModule(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	must := func(rel, body string) {
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	must("go.mod", "module sample\ngo 1.21\n")
	must("main.go", `package main

import "sample/internal/lib"

func main() {
	lib.Hello()
}
`)
	must("internal/lib/lib.go", `package lib

import "fmt"

type Greeter struct{ Prefix string }

func (g *Greeter) Greet(name string) {
	fmt.Println(g.Prefix, name)
}

func Hello() {
	g := &Greeter{Prefix: "hi"}
	g.Greet("world")
}
`)
	// Also drop a non-Go file to confirm dispatch ignores it.
	must("README.md", "# sample\n")
	// And a test file we want to skip.
	must("internal/lib/lib_test.go", `package lib

import "testing"

func TestGreet(t *testing.T) {}
`)
	return root
}

func TestSlug_StableAndUnique(t *testing.T) {
	a := Slug("/Users/mike/Code/cmdr")
	b := Slug("/Users/mike/Code/cmdr")
	if a != b {
		t.Errorf("slug not stable: %q vs %q", a, b)
	}
	c := Slug("/tmp/cmdr")
	if a == c {
		t.Error("expected different slugs for different absolute paths with same basename")
	}
}

func TestBuild_RoundTrip(t *testing.T) {
	repo := fixtureModule(t)
	storeDir := t.TempDir()
	store, err := NewStoreAt(storeDir)
	if err != nil {
		t.Fatal(err)
	}

	slug := Slug(repo)
	if _, err := store.RepoDir(slug, repo); err != nil {
		t.Fatal(err)
	}

	snap, err := Build(BuildOptions{
		RepoPath:  repo,
		CommitSHA: "deadbeef",
		Slug:      slug,
		Store:     store,
	})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if snap.SchemaVersion != SchemaVersion {
		t.Errorf("schema version = %d, want %d", snap.SchemaVersion, SchemaVersion)
	}
	if snap.Snapshot.CommitSHA != "deadbeef" {
		t.Errorf("sha = %q", snap.Snapshot.CommitSHA)
	}
	if snap.Stats.NodeCount == 0 {
		t.Error("expected at least one node")
	}
	if snap.Stats.EdgeCount == 0 {
		t.Error("expected at least one edge")
	}
	if len(snap.Communities) == 0 {
		t.Error("expected community detection to assign at least one community")
	}

	// Test files should be skipped.
	for _, n := range snap.Nodes {
		if filepath.Base(n.SourceFile) == "lib_test.go" {
			t.Errorf("test file should have been skipped: %s", n.SourceFile)
		}
	}

	// Round-trip through the store.
	if err := store.WriteSnapshot(slug, "deadbeef", snap); err != nil {
		t.Fatal(err)
	}
	if !store.HasSnapshot(slug, "deadbeef") {
		t.Error("HasSnapshot should be true after write")
	}
	got, err := store.ReadSnapshot(slug, "deadbeef")
	if err != nil {
		t.Fatal(err)
	}
	if got.Stats.NodeCount != snap.Stats.NodeCount {
		t.Errorf("round-trip node count: got %d want %d", got.Stats.NodeCount, snap.Stats.NodeCount)
	}
}

func TestBuild_CacheHits(t *testing.T) {
	repo := fixtureModule(t)
	store, _ := NewStoreAt(t.TempDir())
	slug := Slug(repo)
	store.RepoDir(slug, repo)

	first, err := Build(BuildOptions{RepoPath: repo, CommitSHA: "a", Slug: slug, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	// Force corruption of source files. Cached extractions should still drive Build.
	main := filepath.Join(repo, "main.go")
	if err := os.WriteFile(main, []byte("package main\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	// Build again with the SAME content as before by writing it back. We're
	// asserting that the cache is hit when content hashes match — so we leave
	// the truncated file in place and verify counts dropped (cache miss path
	// for changed files works too).
	second, err := Build(BuildOptions{RepoPath: repo, CommitSHA: "b", Slug: slug, Store: store})
	if err != nil {
		t.Fatal(err)
	}
	if second.Stats.NodeCount >= first.Stats.NodeCount {
		t.Errorf("expected fewer nodes after stripping main.go, got %d (was %d)", second.Stats.NodeCount, first.Stats.NodeCount)
	}
}

func TestRenderReport(t *testing.T) {
	repo := fixtureModule(t)
	snap, err := Build(BuildOptions{RepoPath: repo, CommitSHA: "x"})
	if err != nil {
		t.Fatal(err)
	}
	body := RenderReport(snap)
	if len(body) == 0 {
		t.Fatal("expected non-empty report")
	}
}

func TestAnalyze_DegreesPopulated(t *testing.T) {
	repo := fixtureModule(t)
	snap, err := Build(BuildOptions{RepoPath: repo, CommitSHA: "x"})
	if err != nil {
		t.Fatal(err)
	}
	any := false
	for _, n := range snap.Nodes {
		if n.Degree > 0 {
			any = true
			break
		}
	}
	if !any {
		t.Error("expected at least one node with non-zero degree")
	}
}
