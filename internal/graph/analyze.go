package graph

import (
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

// labelCommunities derives a human-readable label per community by
// finding the most common top-level path component among its members,
// computes per-community cohesion (modularity contribution), and
// writes everything onto the snapshot.
func labelCommunities(snap *Snapshot, idx []int) {
	a := buildAdjacency(snap)
	groups := map[int][]int{}
	for i, c := range idx {
		groups[c] = append(groups[c], i)
		if i < len(snap.Nodes) {
			snap.Nodes[i].Community = c
		}
	}

	out := make(map[string]Community, len(groups))
	for c, members := range groups {
		nodeIDs := make([]string, 0, len(members))
		paths := map[string]int{}
		for _, mi := range members {
			id := a.ids[mi]
			nodeIDs = append(nodeIDs, id)
			if mi < len(snap.Nodes) {
				if sf := snap.Nodes[mi].SourceFile; sf != "" {
					paths[topComponent(sf)]++
				}
			}
		}
		label := dominantLabel(paths)
		if label == "" {
			label = "community"
		}
		out[itoa(c)] = Community{
			Label:    label,
			NodeIDs:  nodeIDs,
			Cohesion: cohesionFor(a, idx, c),
		}
	}
	snap.Communities = out
}

func topComponent(rel string) string {
	parts := strings.Split(filepath.ToSlash(rel), "/")
	if len(parts) >= 2 {
		// Prefer two-level grouping (e.g. internal/daemon) when
		// available; this matches how cmdr is actually organized.
		return parts[0] + "/" + parts[1]
	}
	return parts[0]
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
