package graph

import (
	"fmt"
	"log"
	"path/filepath"
	"sort"
	"strings"
)

// Analyze annotates a Snapshot in place: computes per-node degree,
// runs two-level Louvain community detection (tier-2 = granular
// clusters, tier-1 = neighborhoods built by collapsing tier-2 into
// super-nodes and re-running Louvain), labels each community/super-
// community by its dominant top-level path component, and fills out
// the Stats, Communities, and SuperCommunities blocks.
func Analyze(snap *Snapshot) {
	computeDegrees(snap)
	communities := detectCommunities(snap)
	labelCommunities(snap, communities)
	superCommunities := detectSuperCommunities(snap, communities)
	labelSuperCommunities(snap, communities, superCommunities)
	computeStats(snap)
}

func computeDegrees(snap *Snapshot) {
	deg := make(map[string]int, len(snap.Nodes))
	for _, e := range snap.Edges {
		deg[e.Source]++
		deg[e.Target]++
	}
	for i := range snap.Nodes {
		snap.Nodes[i].Degree = deg[snap.Nodes[i].ID]
	}
}

// adjacency is the symmetric projection used for Louvain. Self-loops
// are stripped; parallel edges accumulate weight.
type adjacency struct {
	idx    map[string]int    // node id -> dense index
	ids    []string          // dense index -> node id
	out    [][]int           // neighbor indexes (with duplicates for weight)
	totalW float64           // 2m in standard Louvain notation
}

func buildAdjacency(snap *Snapshot) *adjacency {
	a := &adjacency{
		idx: make(map[string]int, len(snap.Nodes)),
		ids: make([]string, 0, len(snap.Nodes)),
	}
	for _, n := range snap.Nodes {
		a.idx[n.ID] = len(a.ids)
		a.ids = append(a.ids, n.ID)
	}
	a.out = make([][]int, len(a.ids))
	for _, e := range snap.Edges {
		s, ok := a.idx[e.Source]
		if !ok {
			continue
		}
		t, ok2 := a.idx[e.Target]
		if !ok2 {
			continue
		}
		if s == t {
			continue
		}
		a.out[s] = append(a.out[s], t)
		a.out[t] = append(a.out[t], s)
		a.totalW += 2 // each undirected edge contributes 2 to the volume
	}
	return a
}

// louvainResolution is the γ in the generalized modularity gain
// formula. γ = 1 is standard Louvain; γ < 1 weakens the penalty on
// community size and produces fewer, larger communities. Tuned down
// from 1.0 because at default resolution a typical JS/TS codebase
// fragments into one cluster per tightly-bound file, which is too
// fine-grained to act as a structural map.
const louvainResolution = 0.4

// runLouvain executes greedy modularity-gain moves on an adjacency
// until convergence and returns the per-node community assignment,
// normalized to contiguous IDs starting from 0. This is the core
// loop, shared by the tier-2 (per-node) pass and the tier-1
// (per-community super-graph) pass.
//
// The "gain" we maximize for placing i into community c is the
// generalized Louvain ΔQ contribution (with i hypothetically removed
// from its own community first):
//
//	gain(c) = w(i,c) - γ * sigma_tot[c] * k_i / 2m
//
// We include the current community in the argmax with the same
// formula (baseline = "put i back where it was"). A strict `>`
// comparison guarantees Q monotonically increases per move, so the
// loop terminates. A maxPasses cap is a belt-and-braces safeguard.
func runLouvain(a *adjacency) []int {
	n := len(a.ids)
	community := make([]int, n)
	for i := range community {
		community[i] = i
	}
	if n == 0 || a.totalW == 0 {
		return community
	}

	ki := make([]float64, n)
	for i := range a.out {
		ki[i] = float64(len(a.out[i]))
	}

	sigmaTot := make([]float64, n)
	for i := range ki {
		sigmaTot[i] = ki[i]
	}

	const maxPasses = 50
	for pass := 0; pass < maxPasses; pass++ {
		changed := false
		for i := 0; i < n; i++ {
			currentC := community[i]

			weights := map[int]float64{}
			for _, j := range a.out[i] {
				weights[community[j]]++
			}

			sigmaTot[currentC] -= ki[i]

			bestC := currentC
			bestGain := weights[currentC] - louvainResolution*sigmaTot[currentC]*ki[i]/a.totalW

			cands := make([]int, 0, len(weights))
			for c := range weights {
				cands = append(cands, c)
			}
			sort.Ints(cands)
			for _, c := range cands {
				if c == currentC {
					continue
				}
				gain := weights[c] - louvainResolution*sigmaTot[c]*ki[i]/a.totalW
				if gain > bestGain {
					bestGain = gain
					bestC = c
				}
			}

			sigmaTot[bestC] += ki[i]
			if bestC != currentC {
				community[i] = bestC
				changed = true
			}
		}
		if !changed {
			break
		}
	}

	// Normalize community ids so they're contiguous starting from 0.
	remap := map[int]int{}
	out := make([]int, n)
	for i, c := range community {
		nc, ok := remap[c]
		if !ok {
			nc = len(remap)
			remap[c] = nc
		}
		out[i] = nc
	}
	return out
}

// detectCommunities runs tier-2 Louvain on the original node-level
// adjacency and returns a per-node community assignment.
func detectCommunities(snap *Snapshot) []int {
	return runLouvain(buildAdjacency(snap))
}

// detectSuperCommunities runs tier-1 Louvain on a graph where each
// tier-2 community has been collapsed into a single super-node. The
// super-graph's edges are the aggregated cross-community edge weights
// from the original adjacency, encoded by index multiplicity (matching
// runLouvain's expectation that out[i] contains a duplicate per unit
// of weight). Returns a per-node tier-1 assignment via mapping back
// through baseIdx.
func detectSuperCommunities(snap *Snapshot, baseIdx []int) []int {
	if len(baseIdx) == 0 {
		return nil
	}
	nC := 0
	for _, c := range baseIdx {
		if c >= nC {
			nC = c + 1
		}
	}
	if nC <= 1 {
		// Single community → super-pass is degenerate; map identically.
		return append([]int(nil), baseIdx...)
	}

	a := buildAdjacency(snap)
	superCounts := make([]map[int]int, nC)
	for i := range superCounts {
		superCounts[i] = map[int]int{}
	}
	for i := 0; i < len(a.out); i++ {
		ci := baseIdx[i]
		for _, j := range a.out[i] {
			cj := baseIdx[j]
			if ci == cj {
				continue
			}
			superCounts[ci][cj]++
		}
	}

	sa := &adjacency{
		idx: make(map[string]int, nC),
		ids: make([]string, nC),
		out: make([][]int, nC),
	}
	for c := 0; c < nC; c++ {
		cid := itoa(c)
		sa.idx[cid] = c
		sa.ids[c] = cid
	}
	for ci, neighbors := range superCounts {
		// Deterministic order so runLouvain's behavior is reproducible.
		nbrIDs := make([]int, 0, len(neighbors))
		for cj := range neighbors {
			nbrIDs = append(nbrIDs, cj)
		}
		sort.Ints(nbrIDs)
		for _, cj := range nbrIDs {
			for x := 0; x < neighbors[cj]; x++ {
				sa.out[ci] = append(sa.out[ci], cj)
				sa.totalW++
			}
		}
	}

	if sa.totalW == 0 {
		// All tier-2 communities are pairwise isolated — super-pass has
		// nothing to merge, so super-id == community-id.
		return append([]int(nil), baseIdx...)
	}

	superC := runLouvain(sa)
	out := make([]int, len(baseIdx))
	for i, c := range baseIdx {
		out[i] = superC[c]
	}
	return out
}

// labelCommunities derives a human-readable label per community using
// the longest common directory prefix of its members' source files.
// When multiple communities collide on the same prefix (common in
// Node.js apps where src/services/{auth,users,orders}/ are separate
// modules), drills into the most-common next-level subdirectory to
// disambiguate. Falls back to a count suffix if drilling can't make
// labels unique.
func labelCommunities(snap *Snapshot, idx []int) {
	a := buildAdjacency(snap)
	groups := map[int][]int{}
	for i, c := range idx {
		groups[c] = append(groups[c], i)
		if i < len(snap.Nodes) {
			snap.Nodes[i].Community = c
		}
	}

	// Collect each community's member node ids + paths. Track file
	// paths and module paths separately so a community that has even
	// one local file gets labeled by its file structure rather than
	// being dragged into "external" fallback by an external module
	// it happens to be connected to. Pure-module communities (no
	// local files at all) fall back to module paths.
	communityPaths := map[int][]string{}
	communityNodeIDs := map[int][]string{}
	for c, members := range groups {
		var filePaths []string
		var modulePaths []string
		nodeIDs := make([]string, 0, len(members))
		for _, mi := range members {
			nodeIDs = append(nodeIDs, a.ids[mi])
			if mi >= len(snap.Nodes) {
				continue
			}
			n := snap.Nodes[mi]
			if n.SourceFile != "" {
				filePaths = append(filePaths, filepath.ToSlash(n.SourceFile))
				continue
			}
			if n.Kind == KindModule {
				if p, ok := n.Attrs["path"].(string); ok && p != "" {
					modulePaths = append(modulePaths, filepath.ToSlash(p))
				}
			}
		}
		if len(filePaths) > 0 {
			communityPaths[c] = filePaths
		} else {
			communityPaths[c] = modulePaths
		}
		communityNodeIDs[c] = nodeIDs
	}

	// Initial labels: longest common directory prefix of each community.
	labels := map[int]string{}
	for c, paths := range communityPaths {
		labels[c] = longestCommonDirPrefix(paths)
		if labels[c] == "" {
			labels[c] = "external"
		}
	}

	// Drill into deeper subdirectories for any communities sharing a
	// label, repeating until no more progress can be made.
	for pass := 0; pass < 4; pass++ {
		labelToCommunities := map[string][]int{}
		for c, label := range labels {
			labelToCommunities[label] = append(labelToCommunities[label], c)
		}
		anyDrilled := false
		for label, cs := range labelToCommunities {
			if len(cs) <= 1 {
				continue
			}
			for _, c := range cs {
				deeper := mostCommonSubdirBeyond(communityPaths[c], label, true)
				if deeper != "" && deeper != labels[c] {
					labels[c] = deeper
					anyDrilled = true
				}
			}
		}
		if !anyDrilled {
			break
		}
	}

	// Anything still colliding gets a stable count suffix.
	finalCollisions := map[string][]int{}
	for c, label := range labels {
		finalCollisions[label] = append(finalCollisions[label], c)
	}
	for label, cs := range finalCollisions {
		if len(cs) <= 1 {
			continue
		}
		// Sort by community id for deterministic numbering.
		sort.Ints(cs)
		for i, c := range cs {
			if i == 0 {
				continue
			}
			labels[c] = fmt.Sprintf("%s (%d)", label, i+1)
		}
	}

	out := make(map[string]Community, len(groups))
	for c := range groups {
		out[itoa(c)] = Community{
			Label:    labels[c],
			NodeIDs:  communityNodeIDs[c],
			Cohesion: cohesionFor(a, idx, c),
		}
	}
	snap.Communities = out
}

// labelSuperCommunities derives a label per super-community using the
// same "longest common dir-prefix + drill into dominant subdir on
// collision" strategy as labelCommunities, but applied to the actual
// source-file paths of every member node (not just child community
// labels). Operating on file paths directly produces more
// distinctive labels than working from already-summarized child
// labels — when two supers share a base prefix, the drill-down
// finds the dominant subtree of each rather than tagging them
// "src (2)" / "src (3)". Also stores child tier-2 IDs so the UI
// can list a super's members without a reverse-lookup pass.
func labelSuperCommunities(snap *Snapshot, communities, superIdx []int) {
	if len(superIdx) == 0 {
		return
	}
	a := buildAdjacency(snap)
	nSuper := 0
	for _, sc := range superIdx {
		if sc >= nSuper {
			nSuper = sc + 1
		}
	}

	superNodeIDs := make(map[int][]string, nSuper)
	superChildren := make(map[int]map[int]struct{}, nSuper)
	superFilePaths := make(map[int][]string, nSuper)
	superModulePaths := make(map[int][]string, nSuper)
	for i := range snap.Nodes {
		if i >= len(superIdx) {
			break
		}
		sc := superIdx[i]
		snap.Nodes[i].SuperCommunity = sc
		n := snap.Nodes[i]
		superNodeIDs[sc] = append(superNodeIDs[sc], n.ID)
		if superChildren[sc] == nil {
			superChildren[sc] = map[int]struct{}{}
		}
		superChildren[sc][communities[i]] = struct{}{}

		if n.SourceFile != "" {
			superFilePaths[sc] = append(superFilePaths[sc], filepath.ToSlash(n.SourceFile))
		} else if n.Kind == KindModule {
			if p, ok := n.Attrs["path"].(string); ok && p != "" {
				superModulePaths[sc] = append(superModulePaths[sc], filepath.ToSlash(p))
			}
		}
	}

	childLists := make(map[int][]string, nSuper)
	for sc, children := range superChildren {
		ids := make([]string, 0, len(children))
		for c := range children {
			ids = append(ids, itoa(c))
		}
		sort.Strings(ids)
		childLists[sc] = ids
	}

	// Initial labels: prefer file paths; fall back to module paths for
	// pure-external supers.
	pathsFor := func(sc int) []string {
		if len(superFilePaths[sc]) > 0 {
			return superFilePaths[sc]
		}
		return superModulePaths[sc]
	}
	labels := map[int]string{}
	for sc := 0; sc < nSuper; sc++ {
		labels[sc] = longestCommonDirPrefix(pathsFor(sc))
		if labels[sc] == "" {
			// No common dir prefix — happens when a super spans multiple
			// top-level subtrees (e.g. src/* + scripts/*). Use the
			// dominant top-level dir of the file paths so the label
			// still reads as something internal. Only fall back to
			// "external" for pure-module supers (no local files at all).
			if len(superFilePaths[sc]) > 0 {
				labels[sc] = dominantTopLevelDir(superFilePaths[sc])
			}
			if labels[sc] == "" {
				labels[sc] = "external"
			}
		}
	}

	// Drill into the dominant subdir of any super sharing a label,
	// repeating until no more progress can be made.
	for pass := 0; pass < 4; pass++ {
		labelToSupers := map[string][]int{}
		for sc, label := range labels {
			labelToSupers[label] = append(labelToSupers[label], sc)
		}
		anyDrilled := false
		for label, scs := range labelToSupers {
			if len(scs) <= 1 {
				continue
			}
			for _, sc := range scs {
				deeper := mostCommonSubdirBeyond(pathsFor(sc), label, false)
				if deeper != "" && deeper != labels[sc] {
					labels[sc] = deeper
					anyDrilled = true
				}
			}
		}
		if !anyDrilled {
			break
		}
	}

	// Last-resort numeric suffix for any still-colliding labels.
	finalCollisions := map[string][]int{}
	for sc, label := range labels {
		finalCollisions[label] = append(finalCollisions[label], sc)
	}
	for label, scs := range finalCollisions {
		if len(scs) <= 1 {
			continue
		}
		sort.Ints(scs)
		for i, sc := range scs {
			if i == 0 {
				continue
			}
			labels[sc] = fmt.Sprintf("%s (%d)", label, i+1)
		}
	}

	out := make(map[string]Community, nSuper)
	for sc, ids := range superNodeIDs {
		out[itoa(sc)] = Community{
			Label:    labels[sc],
			NodeIDs:  ids,
			ChildIDs: childLists[sc],
			Cohesion: cohesionFor(a, superIdx, sc),
		}
	}
	snap.SuperCommunities = out
}

// longestCommonDirPrefix returns the longest path prefix shared by all
// the directories in paths. A single-file community returns the file's
// directory. Returns "" if no common prefix exists or all files are
// in the repo root.
func longestCommonDirPrefix(paths []string) string {
	if len(paths) == 0 {
		return ""
	}
	dirs := make([][]string, 0, len(paths))
	for _, p := range paths {
		dir := filepath.ToSlash(filepath.Dir(p))
		if dir == "." || dir == "" {
			dirs = append(dirs, nil)
			continue
		}
		dirs = append(dirs, strings.Split(dir, "/"))
	}
	if len(dirs[0]) == 0 {
		return ""
	}
	var common []string
	for level := 0; level < len(dirs[0]); level++ {
		ref := dirs[0][level]
		same := true
		for _, d := range dirs[1:] {
			if level >= len(d) || d[level] != ref {
				same = false
				break
			}
		}
		if !same {
			break
		}
		common = append(common, ref)
	}
	return strings.Join(common, "/")
}

// mostCommonSubdirBeyond returns prefix + "/" + a discriminator that
// drills deeper into the community's path structure. Two strategies,
// tried in order:
//
//  1. Most-common next-level subdirectory (when the community spans
//     a structured layout like src/services/{auth,orders,users}).
//  2. Most-common file basename in the immediate prefix dir (when the
//     community is one or more "flat" files like src/services/
//     Inventory.js where a whole class + methods cluster together).
//
// allowFileBasename: pass false when labeling super-communities, where
// drilling down to a specific filename produces misleadingly narrow
// labels (a 400-node super shouldn't read as "src/services/agents/
// AccountingAgent.js"). Tier-2 communities pass true since a single
// file commonly is a meaningful identifier for a small cluster.
func mostCommonSubdirBeyond(paths []string, prefix string, allowFileBasename bool) string {
	pref := prefix + "/"
	subdirs := map[string]int{}
	files := map[string]int{}
	for _, p := range paths {
		rel := strings.TrimPrefix(p, pref)
		if rel == p {
			continue
		}
		if idx := strings.Index(rel, "/"); idx >= 0 {
			subdirs[rel[:idx]]++
		} else {
			files[rel]++
		}
	}
	if best := dominantLabel(subdirs); best != "" {
		return prefix + "/" + best
	}
	if allowFileBasename {
		if best := dominantLabel(files); best != "" {
			return prefix + "/" + best
		}
	}
	return ""
}

// dominantTopLevelDir returns the most common first-segment directory
// across paths. Used as a fallback super-community label when paths
// span multiple top-level subtrees and have no common prefix — e.g.
// a super containing src/* + scripts/* files reports "src" instead
// of misleadingly falling back to "external".
func dominantTopLevelDir(paths []string) string {
	counts := map[string]int{}
	for _, p := range paths {
		idx := strings.Index(p, "/")
		if idx <= 0 {
			continue // root file or empty path
		}
		counts[p[:idx]]++
	}
	return dominantLabel(counts)
}

func dominantLabel(counts map[string]int) string {
	bestK := ""
	bestN := 0
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic tiebreak
	for _, k := range keys {
		if counts[k] > bestN {
			bestN = counts[k]
			bestK = k
		}
	}
	return bestK
}

// cohesionFor estimates how much modularity this community
// contributes: (intra-edges / total-edges) - (sigma_tot/2m)^2.
func cohesionFor(a *adjacency, idx []int, c int) float64 {
	if a.totalW == 0 {
		return 0
	}
	intra := 0.0
	sigmaTot := 0.0
	for i := 0; i < len(a.out); i++ {
		if idx[i] != c {
			continue
		}
		sigmaTot += float64(len(a.out[i]))
		for _, j := range a.out[i] {
			if idx[j] == c {
				intra++
			}
		}
	}
	// intra is double-counted (each undirected edge seen from both ends).
	intra /= 2
	share := sigmaTot / a.totalW
	return (2*intra)/a.totalW - share*share
}

func computeStats(snap *Snapshot) {
	byKind := map[string]int{}
	for _, n := range snap.Nodes {
		byKind[string(n.Kind)]++
	}
	byRel := map[string]int{}
	for _, e := range snap.Edges {
		byRel[string(e.Relation)]++
	}
	snap.Stats = Stats{
		NodeCount:           len(snap.Nodes),
		EdgeCount:           len(snap.Edges),
		ByKind:              byKind,
		ByRelation:          byRel,
		CommunityCount:      len(snap.Communities),
		SuperCommunityCount: len(snap.SuperCommunities),
	}
	logCommunityHistogram(snap)
}

// logCommunityHistogram emits a one-line summary of the community
// size distribution so we can diagnose whether the count is dominated
// by singletons (structural — γ tuning won't help) or by real-but-tiny
// clusters (γ might help, or multi-level Louvain is the fix).
func logCommunityHistogram(snap *Snapshot) {
	if len(snap.Communities) == 0 {
		return
	}
	type sized struct {
		label string
		size  int
	}
	sizes := make([]sized, 0, len(snap.Communities))
	for _, c := range snap.Communities {
		sizes = append(sizes, sized{c.Label, len(c.NodeIDs)})
	}
	sort.Slice(sizes, func(i, j int) bool { return sizes[i].size > sizes[j].size })

	var singleton, small, medium, large int
	for _, s := range sizes {
		switch {
		case s.size == 1:
			singleton++
		case s.size <= 5:
			small++
		case s.size <= 20:
			medium++
		default:
			large++
		}
	}

	topN := 5
	if topN > len(sizes) {
		topN = len(sizes)
	}
	tops := make([]string, 0, topN)
	for i := 0; i < topN; i++ {
		tops = append(tops, fmt.Sprintf("%s=%d", sizes[i].label, sizes[i].size))
	}

	log.Printf("graph: %d communities (in %d super-communities) — singletons=%d small(2-5)=%d medium(6-20)=%d large(21+)=%d; top: %s",
		len(snap.Communities), len(snap.SuperCommunities), singleton, small, medium, large, strings.Join(tops, " "))
}

// itoa is a small helper to avoid importing strconv just for community keys.
func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}
