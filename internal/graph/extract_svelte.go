package graph

import (
	"context"
	"path"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/javascript"
	"github.com/smacker/go-tree-sitter/svelte"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// extractSvelte parses a .svelte file. Svelte's grammar exposes
// <script> blocks as `script_element` nodes containing a `raw_text`
// child that holds the script body verbatim. We pull that body out
// and reparse it with the TS or JS grammar (selected by the
// `lang="ts"` attribute on the start tag), then walk the inner AST
// with the shared TS walker.
//
// Source locations on script-block decls are line-relative to the
// script body, not the .svelte file. Acceptable v1 imprecision.
func extractSvelte(relPath string, content []byte) (*FileExtraction, error) {
	parser := sitter.NewParser()
	parser.SetLanguage(svelte.GetLanguage())
	tree, err := parser.ParseCtx(context.Background(), nil, content)
	if err != nil {
		return &FileExtraction{Language: "svelte"}, nil
	}
	defer tree.Close()

	fx := &FileExtraction{Language: "svelte"}
	fileID := relPath
	fx.Nodes = append(fx.Nodes, Node{
		ID:         fileID,
		Label:      path.Base(relPath),
		Kind:       KindFile,
		Language:   "svelte",
		SourceFile: relPath,
	})

	root := tree.RootNode()
	for i := uint32(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(int(i))
		if c.Type() != "script_element" {
			continue
		}
		walkSvelteScript(fx, c, content, fileID, relPath)
	}
	return fx, nil
}

// walkSvelteScript pulls the raw script body out of a script_element
// node, reparses with the appropriate grammar, then runs the TS-style
// decl walker against it.
func walkSvelteScript(fx *FileExtraction, scriptEl *sitter.Node, content []byte, fileID, relPath string) {
	lang := javascript.GetLanguage()
	language := "js"
	if scriptHasLangTS(scriptEl, content) {
		lang = typescript.GetLanguage()
		language = "ts"
	}

	var raw *sitter.Node
	for i := uint32(0); i < scriptEl.NamedChildCount(); i++ {
		c := scriptEl.NamedChild(int(i))
		if c.Type() == "raw_text" {
			raw = c
			break
		}
	}
	if raw == nil {
		return
	}
	body := content[raw.StartByte():raw.EndByte()]

	parser := sitter.NewParser()
	parser.SetLanguage(lang)
	tree, err := parser.ParseCtx(context.Background(), nil, body)
	if err != nil || tree == nil {
		return
	}
	defer tree.Close()

	walkTSDecls(fx, tree.RootNode(), body, fileID, relPath, language)
}

// scriptHasLangTS scans the start_tag for a `lang="ts"` attribute.
func scriptHasLangTS(scriptEl *sitter.Node, content []byte) bool {
	for i := uint32(0); i < scriptEl.NamedChildCount(); i++ {
		c := scriptEl.NamedChild(int(i))
		if c.Type() != "start_tag" {
			continue
		}
		// start_tag children look like: tag_name, attribute*, end of tag
		for j := uint32(0); j < c.NamedChildCount(); j++ {
			attr := c.NamedChild(int(j))
			if attr.Type() != "attribute" {
				continue
			}
			text := strings.ToLower(attr.Content(content))
			// Match common shapes: lang="ts" / lang='ts' / lang=ts
			if strings.Contains(text, "lang") && strings.Contains(text, "ts") {
				return true
			}
		}
	}
	return false
}
