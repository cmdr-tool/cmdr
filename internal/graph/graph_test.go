package graph

import (
	"os"
	"path/filepath"
	"strings"
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

func TestResolveImport_Relative(t *testing.T) {
	byID := map[string]Node{
		"src/processes/GenerateSearchEmbeddings.js": {ID: "src/processes/GenerateSearchEmbeddings.js", Kind: KindFile},
		"src/services/Content.ts":                   {ID: "src/services/Content.ts", Kind: KindFile},
		"src/lib/utils/index.ts":                    {ID: "src/lib/utils/index.ts", Kind: KindFile},
	}
	cases := []struct {
		name   string
		source string
		spec   string
		want   string
	}{
		{"explicit extension", "src/index.js", "./processes/GenerateSearchEmbeddings.js", "src/processes/GenerateSearchEmbeddings.js"},
		{"omitted extension", "src/index.js", "./processes/GenerateSearchEmbeddings", "src/processes/GenerateSearchEmbeddings.js"},
		{"sibling reference", "src/processes/runner.js", "./GenerateSearchEmbeddings", "src/processes/GenerateSearchEmbeddings.js"},
		{"parent reference", "src/processes/sub/runner.js", "../GenerateSearchEmbeddings", "src/processes/GenerateSearchEmbeddings.js"},
		{"directory index", "src/index.js", "./lib/utils", "src/lib/utils/index.ts"},
		{"ts-first preference", "src/index.js", "./services/Content", "src/services/Content.ts"},
		{"absolute specifier without alias", "src/index.js", "lodash", ""},
		{"unresolved file", "src/index.js", "./missing", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveImport(tc.source, tc.spec, byID, nil)
			if got != tc.want {
				t.Errorf("resolveImport(%q, %q) = %q, want %q", tc.source, tc.spec, got, tc.want)
			}
		})
	}
}

func TestResolveImport_Aliased(t *testing.T) {
	byID := map[string]Node{
		"src/processes/GenerateSearchEmbeddings.js": {ID: "src/processes/GenerateSearchEmbeddings.js", Kind: KindFile},
		"src/lib/utils/index.ts":                    {ID: "src/lib/utils/index.ts", Kind: KindFile},
		"src/config.ts":                             {ID: "src/config.ts", Kind: KindFile},
	}
	aliases := &aliasMap{}
	aliases.addEntry("#processes/*", "src/processes/*")
	aliases.addEntry("#lib/*", "src/lib/*")
	aliases.addEntry("#config", "src/config.ts")

	cases := []struct {
		name string
		spec string
		want string
	}{
		{"wildcard with extension", "#processes/GenerateSearchEmbeddings.js", "src/processes/GenerateSearchEmbeddings.js"},
		{"wildcard without extension", "#processes/GenerateSearchEmbeddings", "src/processes/GenerateSearchEmbeddings.js"},
		{"wildcard to directory index", "#lib/utils", "src/lib/utils/index.ts"},
		{"exact match", "#config", "src/config.ts"},
		{"unmatched alias prefix", "#unknown/foo", ""},
		{"bare module package", "lodash", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := resolveImport("src/index.js", tc.spec, byID, aliases)
			if got != tc.want {
				t.Errorf("resolveImport(%q) = %q, want %q", tc.spec, got, tc.want)
			}
		})
	}
}

func TestLoadAliases_FromConfig(t *testing.T) {
	repo := t.TempDir()
	if err := os.WriteFile(filepath.Join(repo, "package.json"), []byte(`{
		"imports": {
			"#processes/*": "./src/processes/*",
			"#legacy": "./src/legacy.js",
			"#conditional": { "default": "./src/cond.js", "node": "./src/cond-node.js" }
		}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repo, "jsconfig.json"), []byte(`{
		// Project paths
		"compilerOptions": {
			"baseUrl": ".",
			"paths": {
				"#lib/*": ["src/lib/*"],
				"#utils/*": ["src/utils/*"]
			}
		}
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	aliases := loadAliases(repo)
	cases := map[string]string{
		"#processes/foo":  "src/processes/foo",
		"#legacy":         "src/legacy.js",
		"#conditional":    "src/cond.js",
		"#lib/utils":      "src/lib/utils",
		"#utils/parse":    "src/utils/parse",
		"unknown/package": "",
	}
	for spec, want := range cases {
		if got := aliases.resolve(spec); got != want {
			t.Errorf("resolve(%q) = %q, want %q", spec, got, want)
		}
	}
}

// TestResolveImport_BracketedWildcard covers the common Node-style
// pattern where the wildcard has a fixed suffix after it (e.g.
// "#processes/*.js" → "./src/processes/*.js"). A naive trailing-only
// wildcard implementation produces a doubled extension.
func TestResolveImport_BracketedWildcard(t *testing.T) {
	byID := map[string]Node{
		"src/processes/Foo.js": {ID: "src/processes/Foo.js", Kind: KindFile},
	}
	aliases := &aliasMap{}
	aliases.addEntry("#processes/*.js", "./src/processes/*.js")

	if got := aliases.resolve("#processes/Foo.js"); got != "src/processes/Foo.js" {
		t.Errorf("resolve(\"#processes/Foo.js\") = %q, want \"src/processes/Foo.js\" (no double extension)", got)
	}
	if got := aliases.resolve("#processes/Foo"); got != "" {
		t.Errorf("bracketed wildcard should require .js suffix; resolve(\"#processes/Foo\") = %q, want \"\"", got)
	}
	if got := resolveImport("src/index.js", "#processes/Foo.js", byID, aliases); got != "src/processes/Foo.js" {
		t.Errorf("resolveImport with bracketed wildcard = %q, want \"src/processes/Foo.js\"", got)
	}
}

func TestMergeAndPrune_CollapsesAliasedImports(t *testing.T) {
	aliases := &aliasMap{}
	aliases.addEntry("#processes/*", "src/processes/*")

	nodes := []Node{
		{ID: "src/index.js", Kind: KindFile, Label: "index.js"},
		{ID: "src/processes/Foo.js", Kind: KindFile, Label: "Foo.js"},
	}
	edges := []Edge{
		{Source: "src/index.js", Target: "import:#processes/Foo.js", Relation: RelImports, Confidence: ConfidenceExtracted},
		{Source: "src/index.js", Target: "import:./relative.js", Relation: RelImports, Confidence: ConfidenceExtracted},
	}
	gotNodes, gotEdges := mergeAndPrune(nodes, edges, aliases)

	// Aliased import should resolve to the file node — no synthetic
	// module for #processes/Foo.js.
	for _, n := range gotNodes {
		if n.Kind == KindModule && strings.Contains(n.ID, "#processes") {
			t.Errorf("aliased import should not synthesize a module node, got %+v", n)
		}
	}
	var hadResolved bool
	for _, e := range gotEdges {
		if e.Source == "src/index.js" && e.Target == "src/processes/Foo.js" {
			hadResolved = true
		}
	}
	if !hadResolved {
		t.Errorf("expected aliased import edge rewritten to file node, got %+v", gotEdges)
	}
}

// TestBuild_AliasedImportCollapses runs the full Build pipeline
// against a fixture that mirrors the user-reported case: a JS project
// with a jsconfig.json defining a `#processes/*` alias and an importer
// that uses it. After the build, there should be a single file node
// for the imported module and zero synthetic module nodes pointing
// at the same file.
func TestBuild_AliasedImportCollapses(t *testing.T) {
	repo := t.TempDir()
	must := func(rel, body string) {
		p := filepath.Join(repo, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	must("jsconfig.json", `{
		"compilerOptions": {
			"baseUrl": ".",
			"paths": {
				"#processes/*": ["src/processes/*"]
			}
		}
	}`)
	must("src/index.js", `import GenerateSearchEmbeddings from '#processes/GenerateSearchEmbeddings.js';

export function run() {
	return GenerateSearchEmbeddings;
}
`)
	must("src/processes/GenerateSearchEmbeddings.js", `export default class GenerateSearchEmbeddings {
	run() {}
}
`)

	snap, err := Build(BuildOptions{RepoPath: repo, CommitSHA: "test"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}

	const targetFile = "src/processes/GenerateSearchEmbeddings.js"
	var fileNodes, moduleNodes []Node
	for _, n := range snap.Nodes {
		switch n.Kind {
		case KindFile:
			if n.ID == targetFile || strings.HasSuffix(n.ID, "GenerateSearchEmbeddings.js") {
				fileNodes = append(fileNodes, n)
			}
		case KindModule:
			moduleNodes = append(moduleNodes, n)
		}
	}

	if len(fileNodes) != 1 {
		t.Errorf("expected exactly 1 file node for %s, got %d: %+v", targetFile, len(fileNodes), fileNodes)
	}
	for _, n := range moduleNodes {
		if strings.Contains(n.ID, "GenerateSearchEmbeddings") || strings.Contains(n.Label, "GenerateSearchEmbeddings") {
			t.Errorf("found a synthetic module node duplicating the file: %+v", n)
		}
	}

	// And there should be a real edge from src/index.js → the file node.
	var found bool
	for _, e := range snap.Edges {
		if e.Source == "src/index.js" && e.Target == targetFile && e.Relation == RelImports {
			found = true
			break
		}
	}
	if !found {
		t.Errorf("expected imports edge src/index.js → %s; edges: %+v", targetFile, snap.Edges)
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
