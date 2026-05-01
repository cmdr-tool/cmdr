// Package graph builds, stores, and queries code knowledge graphs for
// repos that cmdr monitors. The pipeline is detect → extract → build →
// analyze → snapshot, with each stage operating over the plain types
// declared here. See docs/ADR-0001-knowledge-graph.md.
package graph

import "time"

// Schema version of the on-disk graph.json artifact. Bump when the
// shape of Snapshot, Node, or Edge changes incompatibly.
const SchemaVersion = 1

// NodeKind is the closed enum of node types we model. See ADR.
type NodeKind string

const (
	KindFile       NodeKind = "file"
	KindModule     NodeKind = "module"
	KindFunction   NodeKind = "function"
	KindMethod     NodeKind = "method"
	KindClass      NodeKind = "class"
	KindInterface  NodeKind = "interface"
	KindType       NodeKind = "type"
	KindTable      NodeKind = "table"
	KindColumn     NodeKind = "column"
	KindCollection NodeKind = "collection" // Phase 7: Mongo collections
)

// EdgeRelation is the closed enum of edge relations we model. See ADR.
type EdgeRelation string

const (
	RelContains   EdgeRelation = "contains"
	RelImports    EdgeRelation = "imports"
	RelCalls      EdgeRelation = "calls"
	RelExtends    EdgeRelation = "extends"
	RelImplements EdgeRelation = "implements"
	RelUsesType   EdgeRelation = "uses_type"
	RelForeignKey EdgeRelation = "foreign_key"
	RelAccesses   EdgeRelation = "accesses" // Phase 7: function → collection
	RelJoins      EdgeRelation = "joins"    // Phase 7: collection → collection (reserved, unused in v1)
)

// Confidence reflects how certain we are an edge is real.
// v1 only emits Extracted; LSP enrichment in v2 adds Inferred.
type Confidence string

const (
	ConfidenceExtracted Confidence = "EXTRACTED"
	ConfidenceInferred  Confidence = "INFERRED"
)

// Node is a vertex in the graph: a file, a function, a type, etc.
// Community is the tier-2 (granular) Louvain assignment;
// SuperCommunity is the tier-1 (broad neighborhood) assignment from
// the second-level pass that collapses tier-2 clusters into
// super-nodes and re-runs Louvain.
type Node struct {
	ID             string         `json:"id"`
	Label          string         `json:"label"`
	Kind           NodeKind       `json:"kind"`
	Language       string         `json:"language"`
	SourceFile     string         `json:"source_file"`
	SourceLocation string         `json:"source_location,omitempty"`
	Community      int            `json:"community"`
	SuperCommunity int            `json:"super_community"`
	Degree         int            `json:"degree"`
	Attrs          map[string]any `json:"attrs,omitempty"`
}

// Edge is a directed relationship between two nodes.
type Edge struct {
	Source     string         `json:"source"`
	Target     string         `json:"target"`
	Relation   EdgeRelation   `json:"relation"`
	Confidence Confidence     `json:"confidence"`
	Attrs      map[string]any `json:"attrs,omitempty"`
}

// Community groups nodes detected as a single cluster by Louvain.
// ChildIDs is populated only on super-communities and lists the
// tier-2 community IDs that collapsed into this super-cluster.
type Community struct {
	Label    string   `json:"label"`
	NodeIDs  []string `json:"node_ids"`
	ChildIDs []string `json:"child_ids,omitempty"`
	Cohesion float64  `json:"cohesion"`
}

// Stats summarizes a graph for the snapshot list view and report.
type Stats struct {
	NodeCount           int            `json:"node_count"`
	EdgeCount           int            `json:"edge_count"`
	ByKind              map[string]int `json:"by_kind"`
	ByRelation          map[string]int `json:"by_relation"`
	CommunityCount      int            `json:"community_count"`
	SuperCommunityCount int            `json:"super_community_count"`
}

// Meta is the top-level snapshot metadata block.
type Meta struct {
	RepoPath  string    `json:"repo_path"`
	CommitSHA string    `json:"commit_sha"`
	BuiltAt   time.Time `json:"built_at"`
	Languages []string  `json:"languages"`
}

// Snapshot is the canonical on-disk artifact (graph.json).
type Snapshot struct {
	SchemaVersion    int                  `json:"schema_version"`
	Snapshot         Meta                 `json:"snapshot"`
	Stats            Stats                `json:"stats"`
	Communities      map[string]Community `json:"communities"`
	SuperCommunities map[string]Community `json:"super_communities"`
	Nodes            []Node               `json:"nodes"`
	Edges            []Edge               `json:"edges"`
}

// FileExtraction is what each per-language extractor returns. The
// build stage merges many of these into a Snapshot.
type FileExtraction struct {
	Language string `json:"language"`
	Nodes    []Node `json:"nodes"`
	Edges    []Edge `json:"edges"`
}
