package graph

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Analyze annotates a Snapshot in place: computes per-node degree,
// runs Louvain community detection on the undirected projection,
// labels each community by its dominant top-level path component, and
// fills out the Stats and Communities blocks.
func Analyze(snap *Snapshot) {
	computeDegrees(snap)
	communities := detectCommunities(snap)
	labelCommunities(snap, communities)
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

// detectCommunities is a single-pass Louvain-style assignment: greedy
// modularity-gain moves over a deterministic node ordering until no
// node changes its community. For graphs of a few thousand nodes this
// converges fast and is dependency-free.
//
// The "gain" we maximize for placing i into community c is the
// standard Louvain ΔQ contribution (with i hypothetically removed
// from its own community first):
//
//	gain(c) = w(i,c) - sigma_tot[c] * k_i / 2m
//
// We include the current community in the argmax with the same
// formula (baseline = "put i back where it was"). A strict `>`
// comparison guarantees Q monotonically increases per move, so the
// loop terminates. A maxPasses cap is a belt-and-braces safeguard.
func detectCommunities(snap *Snapshot) []int {
	a := buildAdjacency(snap)
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

			// Remove i from its current community before evaluating any
			// candidate (including currentC itself, which corresponds to
			// "put i back" — its gain is then the baseline to beat).
			sigmaTot[currentC] -= ki[i]

			bestC := currentC
			bestGain := weights[currentC] - sigmaTot[currentC]*ki[i]/a.totalW

			cands := make([]int, 0, len(weights))
			for c := range weights {
				cands = append(cands, c)
			}
			sort.Ints(cands)
			for _, c := range cands {
				if c == currentC {
					continue
				}
				gain := weights[c] - sigmaTot[c]*ki[i]/a.totalW
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

	// Collect each community's member node ids + paths. SourceFile is
	// the primary signal, but we also pull module nodes' Attrs.path so
	// communities of external imports (npm packages, relative imports
	// our extractors couldn't resolve to files) still get meaningful
	// labels rather than the "community" fallback.
	communityPaths := map[int][]string{}
	communityNodeIDs := map[int][]string{}
	for c, members := range groups {
		var paths []string
		nodeIDs := make([]string, 0, len(members))
		for _, mi := range members {
			nodeIDs = append(nodeIDs, a.ids[mi])
			if mi >= len(snap.Nodes) {
				continue
			}
			n := snap.Nodes[mi]
			if n.SourceFile != "" {
				paths = append(paths, filepath.ToSlash(n.SourceFile))
				continue
			}
			if n.Kind == KindModule {
				if p, ok := n.Attrs["path"].(string); ok && p != "" {
					paths = append(paths, filepath.ToSlash(p))
				}
			}
		}
		communityPaths[c] = paths
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
				deeper := mostCommonSubdirBeyond(communityPaths[c], label)
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
func mostCommonSubdirBeyond(paths []string, prefix string) string {
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
	if best := dominantLabel(files); best != "" {
		return prefix + "/" + best
	}
	return ""
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
		NodeCount:      len(snap.Nodes),
		EdgeCount:      len(snap.Edges),
		ByKind:         byKind,
		ByRelation:     byRel,
		CommunityCount: len(snap.Communities),
	}
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
