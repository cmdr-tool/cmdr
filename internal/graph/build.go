package graph

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
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
	case ".vue":
		return extractVue
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
	PhaseTracing    Phase = "tracing"
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
	allNodes, allEdges = mergeAndPrune(allNodes, allEdges, loadAliases(opts.RepoPath))

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
func mergeAndPrune(nodes []Node, edges []Edge, aliases *aliasMap) ([]Node, []Edge) {
	byID := map[string]Node{}
	for _, n := range nodes {
		if _, exists := byID[n.ID]; exists {
			continue
		}
		byID[n.ID] = n
	}

	// Rewrite `imports` edges to point at the actual file node when
	// one exists. Without this the file node and the synthetic module
	// node materialized below are two separate representations of the
	// same JS/TS module. Handles ESM-relative specs (./foo, ../foo)
	// and configured aliases from package.json `imports` and
	// tsconfig/jsconfig `paths`.
	for i, e := range edges {
		if e.Relation != RelImports || !strings.HasPrefix(e.Target, "import:") {
			continue
		}
		spec := strings.TrimPrefix(e.Target, "import:")
		if id := resolveImport(e.Source, spec, byID, aliases); id != "" {
			edges[i].Target = id
		}
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
		byID[e.Target] = Node{
			ID:       e.Target,
			Label:    importLabel(path),
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

// fileExtensions is the set of suffixes we treat as file-extension-shaped
// when deciding how to label import targets. Anything in here, used as
// the suffix-after-last-dot, makes us fall back to using the path's
// basename as the label rather than trimming everything before the dot
// (which would leave just "js" / "ts" / "vue" / etc).
var fileExtensions = map[string]bool{
	"js": true, "ts": true, "mjs": true, "cjs": true,
	"jsx": true, "tsx": true, "py": true, "sql": true,
	"vue": true, "svelte": true, "json": true, "css": true,
	"scss": true, "html": true, "md": true, "go": true,
}

// jsLikeExts are extensions probed when resolving an import that
// omits one. Order matters for index-file resolution — .ts wins over
// .js when both exist (TS-first projects are common).
var jsLikeExts = []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".vue", ".svelte"}

// resolveImport maps an import specifier to a file node ID. Handles
// ESM-relative specs (./foo, ../foo) plus configured aliases loaded
// from package.json `imports` and tsconfig/jsconfig `paths`. Returns
// "" if no file node matches.
func resolveImport(sourceFile, spec string, byID map[string]Node, aliases *aliasMap) string {
	if strings.HasPrefix(spec, "./") || strings.HasPrefix(spec, "../") {
		return probeFileNode(filepath.Join(filepath.Dir(sourceFile), spec), byID)
	}
	if resolved := aliases.resolve(spec); resolved != "" {
		return probeFileNode(resolved, byID)
	}
	return ""
}

// probeFileNode looks up base, base+ext, and base/index+ext against
// the node map and returns the matching file node ID.
func probeFileNode(base string, byID map[string]Node) string {
	base = filepath.ToSlash(filepath.Clean(base))
	if n, ok := byID[base]; ok && n.Kind == KindFile {
		return n.ID
	}
	for _, ext := range jsLikeExts {
		if n, ok := byID[base+ext]; ok && n.Kind == KindFile {
			return n.ID
		}
	}
	for _, ext := range jsLikeExts {
		if n, ok := byID[base+"/index"+ext]; ok && n.Kind == KindFile {
			return n.ID
		}
	}
	return ""
}

// aliasMap holds resolved alias patterns from package.json `imports`
// and tsconfig/jsconfig `paths`. Patterns may contain a single
// trailing `*` wildcard, matching the dominant convention in both
// formats. Conditional/glob exports beyond a default fall-through
// are not modeled.
type aliasMap struct {
	entries []aliasEntry
}

// aliasEntry compiles a single tsconfig/package.json alias pattern
// into a regex that captures the `*` substitution and a target
// template that uses `$1` to splice it back in. Exact-match
// patterns have no capture and a literal target.
type aliasEntry struct {
	re     *regexp.Regexp
	target string
}

// loadAliases reads package.json imports plus tsconfig/jsconfig paths
// from repoPath. Failures fall through silently — alias resolution
// is best-effort; if the config is missing or malformed we just keep
// the synthetic-module fallback.
func loadAliases(repoPath string) *aliasMap {
	m := &aliasMap{}
	m.loadPackageJSONImports(repoPath)
	m.loadTSConfigPaths(repoPath, "tsconfig.json")
	m.loadTSConfigPaths(repoPath, "jsconfig.json")
	return m
}

func (m *aliasMap) loadPackageJSONImports(repoPath string) {
	data, err := os.ReadFile(filepath.Join(repoPath, "package.json"))
	if err != nil {
		return
	}
	var pkg struct {
		Imports map[string]any `json:"imports"`
	}
	if err := json.Unmarshal(data, &pkg); err != nil {
		return
	}
	for k, v := range pkg.Imports {
		target, ok := flattenPackageImportTarget(v)
		if !ok {
			continue
		}
		m.addEntry(k, target)
	}
}

// flattenPackageImportTarget collapses Node's conditional-export
// shapes down to a single string target, preferring `default` then
// `node`. Anything more elaborate is skipped.
func flattenPackageImportTarget(v any) (string, bool) {
	switch x := v.(type) {
	case string:
		return x, true
	case map[string]any:
		if d, ok := x["default"].(string); ok {
			return d, true
		}
		if d, ok := x["node"].(string); ok {
			return d, true
		}
	}
	return "", false
}

// jsoncCommentRe strips // line comments and /* */ block comments.
// Doesn't try to be string-literal-aware — tsconfig files almost
// never contain comment-like sequences inside strings.
var jsoncCommentRe = regexp.MustCompile(`(?m)//[^\n]*|/\*[\s\S]*?\*/`)

func (m *aliasMap) loadTSConfigPaths(repoPath, name string) {
	data, err := os.ReadFile(filepath.Join(repoPath, name))
	if err != nil {
		return
	}
	var cfg struct {
		CompilerOptions struct {
			BaseURL string              `json:"baseUrl"`
			Paths   map[string][]string `json:"paths"`
		} `json:"compilerOptions"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		// tsconfig commonly contains JSONC comments — strip and retry.
		if err := json.Unmarshal(jsoncCommentRe.ReplaceAll(data, nil), &cfg); err != nil {
			return
		}
	}
	base := cfg.CompilerOptions.BaseURL
	if base == "" {
		base = "."
	}
	for k, vals := range cfg.CompilerOptions.Paths {
		if len(vals) == 0 {
			continue
		}
		// First entry wins. tsconfig allows fallback paths but file
		// nodes are unique, so probing each is wasted work.
		target := filepath.ToSlash(filepath.Clean(filepath.Join(base, vals[0])))
		m.addEntry(k, target)
	}
}

// addEntry compiles a key/target pair from a config file into an
// anchored regex with a single capture group. Both formats we
// support (package.json `imports` and tsconfig `paths`) restrict
// patterns to one `*` per side, which maps cleanly to `(.*)` in
// the regex and `$1` in the replacement template.
func (m *aliasMap) addEntry(key, target string) {
	// Normalize so package.json's "./src/foo" matches jsconfig's "src/foo"
	// and both line up with our file-ID format (no leading ./).
	target = filepath.ToSlash(filepath.Clean(target))
	pattern := regexp.QuoteMeta(key)
	if strings.Contains(pattern, `\*`) {
		pattern = strings.Replace(pattern, `\*`, `(.*)`, 1)
		target = strings.Replace(target, "*", "$1", 1)
	}
	re, err := regexp.Compile(`^` + pattern + `$`)
	if err != nil {
		// Patterns come from JSON config; QuoteMeta should make this
		// unreachable. Skip silently rather than panic on bad input.
		return
	}
	m.entries = append(m.entries, aliasEntry{re: re, target: target})
}

// resolve maps a spec like "#processes/Foo.js" to the repo-relative
// path the first matching alias points at. Returns "" on no match.
func (m *aliasMap) resolve(spec string) string {
	if m == nil {
		return ""
	}
	for _, e := range m.entries {
		if e.re.MatchString(spec) {
			return e.re.ReplaceAllString(spec, e.target)
		}
	}
	return ""
}

// importLabel produces a human-readable label for an import target.
// Handles three shapes:
//   - Go FQN with exported symbol: pkg.Symbol → "Symbol"
//   - Path with file extension:    ./foo/bar.js → "bar.js" (basename)
//   - Bare module path:            github.com/foo/bar → full path
func importLabel(path string) string {
	idx := strings.LastIndex(path, ".")
	if idx <= 0 {
		// No dot or leading dot — return as-is.
		return path
	}
	suffix := path[idx+1:]
	if strings.Contains(suffix, "/") {
		// e.g. "github.com/foo/bar" — the dot is in the host portion,
		// not a symbol/extension separator. Keep the full path.
		return path
	}
	if fileExtensions[strings.ToLower(suffix)] {
		// Path with explicit extension: ./foo/bar.js → "bar.js"
		return filepath.Base(path)
	}
	// Symbol-shaped suffix: pkg.Symbol → "Symbol", utils.foo → "foo"
	return suffix
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
