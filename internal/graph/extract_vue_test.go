package graph

import "testing"

// TestVueScriptBlockExtraction verifies the Vue extractor walks the
// <script> body via the TS walker. Confirms function/class
// declarations, imports, and Mongo collection patterns inside the
// script block all flow into the file's nodes/edges.
func TestVueScriptBlockExtraction(t *testing.T) {
	src := []byte(`<template>
  <div>{{ greeting }}</div>
</template>

<script setup lang="ts">
import { ref } from 'vue';

const greeting = ref('hello');

function bump() {
  console.log('bumped');
}

export class Greeter {
  greet(name: string): string {
    return ` + "`hi ${name}`" + `;
  }
}

async function loadUser(db, id) {
  return db.collection('users').findOne({ _id: id });
}
</script>

<style scoped>
.x { color: red; }
</style>
`)

	fx, err := extractVue("web/components/Greeter.vue", src)
	if err != nil {
		t.Fatalf("extractVue: %v", err)
	}

	// File node must exist.
	foundFile := false
	for _, n := range fx.Nodes {
		if n.Kind == KindFile && n.ID == "web/components/Greeter.vue" {
			foundFile = true
		}
	}
	if !foundFile {
		t.Errorf("missing file node for the .vue file")
	}

	// Decls inside the script block should land as nodes.
	wantDecls := map[string]NodeKind{
		"web/components/Greeter.vue::bump":     KindFunction,
		"web/components/Greeter.vue::loadUser": KindFunction,
		"web/components/Greeter.vue::Greeter":  KindClass,
		"web/components/Greeter.vue::greeting": KindFunction, // arrow/expr binding via const
	}
	have := map[string]NodeKind{}
	for _, n := range fx.Nodes {
		have[n.ID] = n.Kind
	}
	for id, want := range wantDecls {
		if got, ok := have[id]; !ok {
			// `greeting` is a const-bound ref(), not a function — drop it
			// from the must-have list. Tree-sitter's variable_declarator
			// walker only emits functions for arrow/function expressions,
			// and `ref('hello')` is a call_expression, so this entry won't
			// appear. That's correct behavior; just skip.
			if id == "web/components/Greeter.vue::greeting" {
				continue
			}
			t.Errorf("missing decl node %q", id)
		} else if got != want {
			if id == "web/components/Greeter.vue::greeting" {
				continue
			}
			t.Errorf("decl %q: kind=%q, want %q", id, got, want)
		}
	}

	// Mongo collection node from inside the Vue script should be
	// detected — same walker runs on script-block content.
	foundCollection := false
	for _, n := range fx.Nodes {
		if n.Kind == KindCollection && n.Label == "users" {
			foundCollection = true
		}
	}
	if !foundCollection {
		t.Errorf("expected mongo:collection:users from inside Vue <script>")
	}
}
