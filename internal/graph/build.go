package graph

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
)

// Extractor runs a single file extraction. Returned errors abort the
// build; an extractor that wants to skip a file should return an empty
// FileExtraction with nil error.
type Extractor func(relPath string, content []byte) (*FileExtraction, error)

// isJSTestFile recognizes the common .test.{ext} / .spec.{ext}
// patterns that signal a test file.
func isJSTestFile(name string) bool {
	for _, ext := range []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs"} {
		if strings.HasSuffix(name, ".test"+ext) || strings.HasSuffix(name, ".spec"+ext) {
			return true
		}
	}
	return false
}

// dispatchByExt maps file extensions to their extractor. Phase 6
// added TS/JS/Svelte via tree-sitter; Go still uses stdlib parser.
// SQL is on hold — the available SQL grammars have packaging
// issues with their Go bindings; revisit when we tackle SchemaFacet.
func dispatchByExt(relPath string) Extractor {
	switch strings.ToLower(filepath.Ext(relPath)) {
	case ".go":
		return extractGo
	case ".ts", ".tsx":
		// .d.ts files match this case naturally — filepath.Ext returns
		// just ".ts" for foo.d.ts.
		return extractTS
	case ".js", ".mjs", ".cjs":
		return extractJS
	case ".svelte":
		return extractSvelte
	case ".py":
		return extractPython
	}
	return nil
}

// skipDir filters directories that shouldn't be walked.
var skipDir = map[string]bool{
	".git":         true,
	"node_modules": true,
	"vendor":       true,
	".svelte-kit":  true,
	"dist":         true,
	"build":        true,
	".next":        true,
	".cache":       true,
	"target":       true,
	"__pycache__":  true,
}

// detectFiles walks repoPath and returns paths (relative to the repo
// root) for files we have an extractor for.
func detectFiles(repoPath string) ([]string, error) {
	var out []string
	err := filepath.WalkDir(repoPath, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if skipDir[name] {
				return filepath.SkipDir
			}
			if strings.HasPrefix(name, ".") && name != "." {
				return filepath.SkipDir
			}
			return nil
		}
		// Skip test files — they bloat the graph without adding
		// structural understanding of the codebase. Covers Go's
		// _test.go convention plus the JS/TS .test./.spec. patterns.
		if strings.HasSuffix(name, "_test.go") {
			return nil
		}
		if isJSTestFile(name) {
			return nil
		}
		if dispatchByExt(name) == nil {
			return nil
		}
		rel, err := filepath.Rel(repoPath, p)
		if err != nil {
			return nil
		}
		out = append(out, rel)
		return nil
	})
	return out, err
}

// Phase names a stage of the build pipeline as observed by an
// OnProgress callback. The enum is shared with the daemon's SSE
// events so frontend and backend stay in sync.
type Phase string

const (
	PhaseStarted    Phase = "started"
	PhaseExtracting Phase = "extracting"
	PhaseBuilding   Phase = "building"
	PhaseClustering Phase = "clustering"
	PhaseWriting    Phase = "writing"
	PhaseComplete   Phase = "complete"
	PhaseFailed     Phase = "failed"
)

// BuildOptions controls how a graph is assembled. Snapshot-level
// metadata (CommitSHA, BuiltAt) is supplied here; the pipeline
// itself doesn't talk to git.
type BuildOptions struct {
	RepoPath  string
	CommitSHA string
	Slug      string
	Store     *Store // optional: enables the per-file content-hash cache

	// OnProgress, if set, is called at each pipeline phase. Used by
	// the daemon to publish SSE events; nil in tests.
	OnProgress func(phase Phase, percent int)
}

// Build runs the full pipeline against repoPath: detect → extract →
// build → analyze → snapshot in memory. Caller persists the result.
func Build(opts BuildOptions) (*Snapshot, error) {
	if opts.RepoPath == "" {
		return nil, fmt.Errorf("graph: build: repo path required")
	}
	progress := opts.OnProgress
	if progress == nil {
		progress = func(Phase, int) {}
	}

	files, err := detectFiles(opts.RepoPath)
	if err != nil {
		return nil, fmt.Errorf("graph: detect: %w", err)
	}
	progress(PhaseExtracting, 20)

	languages := map[string]struct{}{}
	var allNodes []Node
	var allEdges []Edge

	for _, rel := range files {
		abs := filepath.Join(opts.RepoPath, rel)
		content, err := os.ReadFile(abs)
		if err != nil {
			continue
		}

		var fx *FileExtraction

		if opts.Store != nil && opts.Slug != "" {
			key := CacheKey(content)
			if cached, ok := opts.Store.LoadCachedExtraction(opts.Slug, key); ok {
				fx = cached
			}
		}

		if fx == nil {
			ext := dispatchByExt(rel)
			if ext == nil {
				continue
			}
			fx, err = ext(rel, content)
			if err != nil {
				return nil, fmt.Errorf("graph: extract %s: %w", rel, err)
			}
			if opts.Store != nil && opts.Slug != "" {
				_ = opts.Store.SaveCachedExtraction(opts.Slug, CacheKey(content), fx)
			}
		}

		if fx == nil {
			continue
		}
		if fx.Language != "" {
			languages[fx.Language] = struct{}{}
		}
		allNodes = append(allNodes, fx.Nodes...)
		allEdges = append(allEdges, fx.Edges...)
	}

	progress(PhaseBuilding, 50)
	allNodes, allEdges = mergeAndPrune(allNodes, allEdges)

	snap := &Snapshot{
		SchemaVersion: SchemaVersion,
		Snapshot: Meta{
			RepoPath:  opts.RepoPath,
			CommitSHA: opts.CommitSHA,
			Languages: sortedKeys(languages),
		},
		Nodes: allNodes,
		Edges: allEdges,
	}
	progress(PhaseClustering, 80)
	Analyze(snap)
	return snap, nil
}

// mergeAndPrune deduplicates nodes by ID, materializes synthetic
// "import:..." targets as module nodes when they're referenced, and
// drops edges whose target doesn't resolve to a known node.
func mergeAndPrune(nodes []Node, edges []Edge) ([]Node, []Edge) {
	byID := map[string]Node{}
	for _, n := range nodes {
		if _, exists := byID[n.ID]; exists {
			continue
		}
		byID[n.ID] = n
	}

	// Materialize import targets that real edges reference.
	for _, e := range edges {
		if !strings.HasPrefix(e.Target, "import:") {
			continue
		}
		if _, ok := byID[e.Target]; ok {
			continue
		}
		path := strings.TrimPrefix(e.Target, "import:")
		// import:<pkg>.<Symbol> stays as-is; bare import:<pkg> becomes a module node.
		label := path
		if idx := strings.LastIndex(path, "."); idx > 0 && !strings.Contains(path[idx+1:], "/") {
			label = path[idx+1:]
		}
		byID[e.Target] = Node{
			ID:       e.Target,
			Label:    label,
			Kind:     KindModule,
			Language: "go",
			Attrs: map[string]any{
				"external": true,
				"path":     path,
			},
		}
	}

	out := make([]Node, 0, len(byID))
	for _, n := range byID {
		out = append(out, n)
	}

	keptEdges := make([]Edge, 0, len(edges))
	for _, e := range edges {
		if _, ok := byID[e.Source]; !ok {
			continue
		}
		if _, ok := byID[e.Target]; !ok {
			continue
		}
		keptEdges = append(keptEdges, e)
	}

	return out, keptEdges
}

func sortedKeys(m map[string]struct{}) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Use a stable, alphabetical order for deterministic output.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}
