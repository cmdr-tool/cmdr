package graph

import (
	"fmt"
	"sort"
	"strings"
)

// RenderReport builds a human-readable markdown summary of the
// snapshot, mirroring the same data as graph.json but in a form
// suitable for skimming or feeding to an LLM later. The exact format
// is intentionally simple — the source of truth remains graph.json.
func RenderReport(snap *Snapshot) []byte {
	var b strings.Builder
	fmt.Fprintf(&b, "# Knowledge Graph — %s\n\n", snap.Snapshot.CommitSHA)
	fmt.Fprintf(&b, "_Repo:_ `%s`\n\n", snap.Snapshot.RepoPath)
	if !snap.Snapshot.BuiltAt.IsZero() {
		fmt.Fprintf(&b, "_Built:_ %s\n\n", snap.Snapshot.BuiltAt.UTC().Format("2006-01-02 15:04:05 UTC"))
	}
	if len(snap.Snapshot.Languages) > 0 {
		fmt.Fprintf(&b, "_Languages:_ %s\n\n", strings.Join(snap.Snapshot.Languages, ", "))
	}

	b.WriteString("## Stats\n\n")
	fmt.Fprintf(&b, "- Nodes: **%d**\n", snap.Stats.NodeCount)
	fmt.Fprintf(&b, "- Edges: **%d**\n", snap.Stats.EdgeCount)
	fmt.Fprintf(&b, "- Communities: **%d**\n\n", snap.Stats.CommunityCount)

	if len(snap.Stats.ByKind) > 0 {
		b.WriteString("### By kind\n\n")
		writeSortedCounts(&b, snap.Stats.ByKind)
	}
	if len(snap.Stats.ByRelation) > 0 {
		b.WriteString("\n### By relation\n\n")
		writeSortedCounts(&b, snap.Stats.ByRelation)
	}

	b.WriteString("\n## Communities\n\n")
	keys := make([]string, 0, len(snap.Communities))
	for k := range snap.Communities {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		c := snap.Communities[k]
		fmt.Fprintf(&b, "- **%s** (%s) — %d nodes, cohesion %.3f\n", c.Label, k, len(c.NodeIDs), c.Cohesion)
	}

	b.WriteString("\n## God nodes (top 10 by degree)\n\n")
	type ranked struct {
		id     string
		label  string
		kind   NodeKind
		degree int
	}
	all := make([]ranked, 0, len(snap.Nodes))
	for _, n := range snap.Nodes {
		all = append(all, ranked{n.ID, n.Label, n.Kind, n.Degree})
	}
	sort.Slice(all, func(i, j int) bool {
		if all[i].degree != all[j].degree {
			return all[i].degree > all[j].degree
		}
		return all[i].id < all[j].id
	})
	limit := 10
	if len(all) < limit {
		limit = len(all)
	}
	for i := 0; i < limit; i++ {
		r := all[i]
		fmt.Fprintf(&b, "- `%s` (%s, degree %d)\n", r.id, r.kind, r.degree)
	}

	return []byte(b.String())
}

func writeSortedCounts(b *strings.Builder, m map[string]int) {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(b, "- %s: %d\n", k, m[k])
	}
}
