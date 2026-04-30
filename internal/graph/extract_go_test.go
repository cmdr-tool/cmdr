package graph

import (
	"strings"
	"testing"
)

const sampleGo = `package sample

import (
	"fmt"
	"strings"
)

type Greeter struct {
	Prefix string
}

func (g *Greeter) Hello(name string) string {
	return g.Prefix + ": " + strings.ToUpper(name)
}

func Run() {
	g := &Greeter{Prefix: "hi"}
	fmt.Println(g.Hello("world"))
	helper()
}

func helper() {}
`

func TestGoExtractor_Basics(t *testing.T) {
	fx, err := extractGo("sample.go", []byte(sampleGo))
	if err != nil {
		t.Fatalf("Go: %v", err)
	}
	if fx.Language != "go" {
		t.Fatalf("language = %q, want go", fx.Language)
	}

	// File node + Greeter type + Hello method + Run + helper = 5 nodes.
	wantKinds := map[NodeKind]int{
		KindFile:     1,
		KindClass:    1,
		KindMethod:   1,
		KindFunction: 2,
	}
	gotKinds := map[NodeKind]int{}
	for _, n := range fx.Nodes {
		gotKinds[n.Kind]++
	}
	for k, want := range wantKinds {
		if gotKinds[k] != want {
			t.Errorf("kind %s: got %d, want %d (all kinds: %v)", k, gotKinds[k], want, gotKinds)
		}
	}

	// Imports edges
	importTargets := map[string]bool{}
	for _, e := range fx.Edges {
		if e.Relation == RelImports {
			importTargets[e.Target] = true
		}
	}
	for _, want := range []string{"import:fmt", "import:strings"} {
		if !importTargets[want] {
			t.Errorf("missing import edge for %s", want)
		}
	}

	// Same-file call: Run -> helper
	wantSameFile := false
	for _, e := range fx.Edges {
		if e.Relation == RelCalls && strings.HasSuffix(e.Source, "::Run") && strings.HasSuffix(e.Target, "::helper") {
			wantSameFile = true
		}
	}
	if !wantSameFile {
		t.Error("expected same-file call edge Run -> helper")
	}

	// External call resolved via import alias: Hello -> import:strings.ToUpper
	wantExternalToUpper := false
	for _, e := range fx.Edges {
		if e.Relation == RelCalls && e.Target == "import:strings.ToUpper" {
			wantExternalToUpper = true
		}
	}
	if !wantExternalToUpper {
		t.Error("expected call edge to import:strings.ToUpper")
	}

	// Method should NOT have a uses_type edge back to its receiver
	// type — that's tautological with the receiver→method `contains`
	// edge and was creating mirror entries in the sidebar.
	for _, e := range fx.Edges {
		if e.Relation == RelUsesType && strings.HasSuffix(e.Source, "::Greeter.Hello") && strings.HasSuffix(e.Target, "::Greeter") {
			t.Error("unexpected uses_type/receiver edge — should be dropped")
		}
	}
}

func TestGoExtractor_DropsAmbiguousCalls(t *testing.T) {
	src := `package sample
func A() {
	mystery()  // not declared in this file, not from a known import
}`
	fx, err := extractGo("a.go", []byte(src))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range fx.Edges {
		if e.Relation == RelCalls {
			t.Fatalf("expected no call edges, got: %+v", e)
		}
	}
}

func TestGoExtractor_HandlesParseErrors(t *testing.T) {
	// Truncated source — parser will error; we should still get a clean
	// (possibly empty) extraction, never a propagated error.
	fx, err := extractGo("broken.go", []byte("package x\nfunc Broken() {"))
	if err != nil {
		t.Fatalf("expected nil error on parse failure, got: %v", err)
	}
	if fx == nil {
		t.Fatal("expected non-nil extraction")
	}
}
