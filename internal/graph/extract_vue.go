package graph

import (
	"path"
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_javascript "github.com/tree-sitter/tree-sitter-javascript/bindings/go"
	tree_sitter_typescript "github.com/tree-sitter/tree-sitter-typescript/bindings/go"
	tree_sitter_vue "github.com/cmdr-tool/cmdr/internal/graph/vendored/tree-sitter-vue/bindings/go"
)

// extractVue parses a .vue Single File Component. Vue's grammar
// exposes the structural blocks (`<template>`, `<script>`, `<style>`)
// as `template_element`, `script_element`, `style_element` nodes —
// each with a `raw_text` child holding the block body verbatim.
// Mirrors extract_svelte.go since Vue and Svelte SFCs share the same
// "framing tags + opaque script body" shape.
//
// Source locations on script-block decls are line-relative to the
// script body, not the .vue file. Same v1 imprecision as Svelte.
func extractVue(relPath string, content []byte) (*FileExtraction, error) {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(tree_sitter_vue.Language())); err != nil {
		return &FileExtraction{Language: "vue"}, nil
	}
	tree := parser.Parse(content, nil)
	if tree == nil {
		return &FileExtraction{Language: "vue"}, nil
	}
	defer tree.Close()

	fx := &FileExtraction{Language: "vue"}
	fileID := relPath
	fx.Nodes = append(fx.Nodes, Node{
		ID:         fileID,
		Label:      path.Base(relPath),
		Kind:       KindFile,
		Language:   "vue",
		SourceFile: relPath,
	})

	root := tree.RootNode()
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		if c.Kind() != "script_element" {
			continue
		}
		walkVueScript(fx, c, content, fileID, relPath)
	}
	return fx, nil
}

// walkVueScript pulls the raw script body out of a script_element node,
// reparses it with TS or JS depending on the lang attribute, then runs
// the shared TS-style decl walker against it.
func walkVueScript(fx *FileExtraction, scriptEl *sitter.Node, content []byte, fileID, relPath string) {
	lang := sitter.NewLanguage(tree_sitter_javascript.Language())
	language := "js"
	if vueScriptHasLangTS(scriptEl, content) {
		lang = sitter.NewLanguage(tree_sitter_typescript.LanguageTypescript())
		language = "ts"
	}

	var raw *sitter.Node
	for i := uint(0); i < scriptEl.NamedChildCount(); i++ {
		c := scriptEl.NamedChild(i)
		if c.Kind() == "raw_text" {
			raw = c
			break
		}
	}
	if raw == nil {
		return
	}
	body := content[raw.StartByte():raw.EndByte()]

	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(lang); err != nil {
		return
	}
	tree := parser.Parse(body, nil)
	if tree == nil {
		return
	}
	defer tree.Close()

	walkTSDecls(fx, tree.RootNode(), body, fileID, relPath, language)
}

// vueScriptHasLangTS scans the start_tag for a `lang="ts"` attribute.
// Vue 3 SFC patterns include `<script setup lang="ts">`, `<script lang="ts">`,
// and `<script>` (no lang, defaults to JS).
func vueScriptHasLangTS(scriptEl *sitter.Node, content []byte) bool {
	for i := uint(0); i < scriptEl.NamedChildCount(); i++ {
		c := scriptEl.NamedChild(i)
		if c.Kind() != "start_tag" {
			continue
		}
		for j := uint(0); j < c.NamedChildCount(); j++ {
			attr := c.NamedChild(j)
			if attr.Kind() != "attribute" {
				continue
			}
			text := strings.ToLower(attr.Utf8Text(content))
			if strings.Contains(text, "lang") && strings.Contains(text, "ts") {
				return true
			}
		}
	}
	return false
}
