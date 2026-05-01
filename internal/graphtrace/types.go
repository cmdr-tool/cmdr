// Package graphtrace runs an LLM-augmented analysis pass on top of a
// deterministic graph snapshot to produce named data flow traces. The
// deterministic graph remains the structural source of truth; traces
// are an interpretive overlay that references graph nodes by ID.
package graphtrace

// Provenance distinguishes steps grounded in literal AST relationships
// from those inferred by reading code. Surfaces visually so the user
// knows what was found vs guessed.
type Provenance string

const (
	ProvenanceExtracted Provenance = "extracted"
	ProvenanceInferred  Provenance = "inferred"
)

// RequirementKind classifies what a step needs to operate.
type RequirementKind string

const (
	RequirementEnv      RequirementKind = "env"      // env var, e.g. GEMINI_API_KEY
	RequirementConfig   RequirementKind = "config"   // config value loaded at startup
	RequirementInstance RequirementKind = "instance" // instance field, e.g. this.client
	RequirementImport   RequirementKind = "import"   // imported module/package
	RequirementType     RequirementKind = "type"     // type/class reference
)

// Requirement is a precondition for a step. Renders as an annotation on
// the step's box in the visualization, not as its own node in the flow.
type Requirement struct {
	Kind        RequirementKind `json:"kind"`
	Label       string          `json:"label"`
	NodeID      string          `json:"node_id,omitempty"`
	Description string          `json:"description,omitempty"`
	SourceFile  string          `json:"source_file,omitempty"`
	SourceLine  int             `json:"source_line,omitempty"`
	Provenance  Provenance      `json:"provenance"`
}

// NextStep is an edge in the trace's internal DAG. Branches carry a
// condition label (e.g. "validation succeeds", "on cache miss").
type NextStep struct {
	To        string `json:"to"`
	Condition string `json:"condition,omitempty"`
}

// TraceStep is one node in a trace's flow DAG.
type TraceStep struct {
	ID          string        `json:"id"`
	NodeID      string        `json:"node_id,omitempty"` // ref into graph.json; empty = conceptual
	Label       string        `json:"label"`
	Description string        `json:"description,omitempty"`
	Provenance  Provenance    `json:"provenance"`
	Next        []NextStep    `json:"next,omitempty"`
	Requires    []Requirement `json:"requires,omitempty"`
	SourceFile  string        `json:"source_file,omitempty"`
	SourceLine  int           `json:"source_line,omitempty"`
}

// Trace is a single named data flow.
type Trace struct {
	Name        string      `json:"name"`
	Description string      `json:"description,omitempty"`
	Entry       string      `json:"entry"`
	Steps       []TraceStep `json:"steps"`
}

// Result is the top-level output of a trace run.
type Result struct {
	RepoSlug  string  `json:"repo_slug"`
	CommitSHA string  `json:"commit_sha"`
	Traces    []Trace `json:"traces"`
}
