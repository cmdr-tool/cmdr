package graph

import (
	"context"
	"fmt"
	"path"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/typescript/tsx"
	"github.com/smacker/go-tree-sitter/typescript/typescript"
)

// extractTS parses a TypeScript file. Selects the typescript or tsx
// grammar based on extension. Emits the same node/edge shape as the
// Go extractor so the build pipeline doesn't care about language.
func extractTS(relPath string, content []byte) (*FileExtraction, error) {
	lang := typescript.GetLanguage()
	if strings.HasSuffix(relPath, ".tsx") {
		lang = tsx.GetLanguage()
	}
	return extractWithTSGrammar(relPath, content, lang, "ts")
}

// extractWithTSGrammar parses content with the given TS/JS-family
// grammar and emits a FileExtraction with one file node + walked decls.
func extractWithTSGrammar(relPath string, content []byte, lang *sitter.Language, language string) (*FileExtraction, error) {
	parser := sitter.NewParser()
	parser.SetLanguage(lang)
	tree, err := parser.ParseCtx(context.Background(), nil, content)
	if err != nil {
		return &FileExtraction{Language: language}, nil
	}
	defer tree.Close()

	fx := &FileExtraction{Language: language}
	fileID := relPath
	fx.Nodes = append(fx.Nodes, Node{
		ID:         fileID,
		Label:      path.Base(relPath),
		Kind:       KindFile,
		Language:   language,
		SourceFile: relPath,
	})

	walkTSDecls(fx, tree.RootNode(), content, fileID, relPath, language)
	return fx, nil
}

// walkTSDecls walks a TS/JS-shaped AST root and emits decls + edges
// into fx. Used by both the file-level extractors (extract_ts.go,
// extract_js.go) and by extract_svelte.go which parses the script
// block separately and needs to merge its decls into the .svelte
// file's node tree.
func walkTSDecls(fx *FileExtraction, root *sitter.Node, content []byte, fileID, relPath, language string) {
	imports := map[string]string{} // local name → module specifier
	declSymbols := map[string]string{}

	// First pass: imports + top-level declared symbol names.
	for i := uint32(0); i < root.NamedChildCount(); i++ {
		child := root.NamedChild(int(i))
		switch child.Type() {
		case "import_statement":
			collectTSImports(fx, fileID, child, content, imports)
		case "function_declaration":
			if name := nameOf(child, content); name != "" {
				declSymbols[name] = relPath + "::" + name
			}
		case "class_declaration", "interface_declaration", "type_alias_declaration":
			if name := nameOf(child, content); name != "" {
				declSymbols[name] = relPath + "::" + name
			}
		case "lexical_declaration", "variable_declaration":
			collectTSVarDeclSymbols(child, content, relPath, declSymbols)
		case "export_statement":
			inner := exportInner(child)
			if inner == nil {
				continue
			}
			switch inner.Type() {
			case "function_declaration", "class_declaration", "interface_declaration", "type_alias_declaration":
				if name := nameOf(inner, content); name != "" {
					declSymbols[name] = relPath + "::" + name
				}
			case "lexical_declaration", "variable_declaration":
				collectTSVarDeclSymbols(inner, content, relPath, declSymbols)
			}
		}
	}

	// Second pass: emit nodes + edges.
	for i := uint32(0); i < root.NamedChildCount(); i++ {
		child := root.NamedChild(int(i))
		emitTSDecl(fx, child, content, fileID, relPath, language, declSymbols, imports, false)
	}
}

// emitTSDecl handles one top-level (or export-wrapped) declaration.
// `exported` propagates through export_statement wrappers so we record
// it on the produced node attrs.
func emitTSDecl(fx *FileExtraction, n *sitter.Node, content []byte, fileID, relPath, language string, declSymbols, imports map[string]string, exported bool) {
	switch n.Type() {
	case "export_statement":
		inner := exportInner(n)
		if inner != nil {
			emitTSDecl(fx, inner, content, fileID, relPath, language, declSymbols, imports, true)
		}
	case "function_declaration":
		emitTSFunction(fx, n, content, fileID, relPath, language, declSymbols, imports, exported, false)
	case "class_declaration":
		emitTSClass(fx, n, content, fileID, relPath, language, declSymbols, imports, exported)
	case "interface_declaration":
		emitTSSimpleDecl(fx, n, content, fileID, relPath, language, KindInterface, exported)
	case "type_alias_declaration":
		emitTSSimpleDecl(fx, n, content, fileID, relPath, language, KindType, exported)
	case "lexical_declaration", "variable_declaration":
		emitTSVarDecl(fx, n, content, fileID, relPath, language, declSymbols, imports, exported)
	}
}

func emitTSFunction(fx *FileExtraction, n *sitter.Node, content []byte, fileID, relPath, language string, declSymbols, imports map[string]string, exported bool, isMethod bool) {
	name := nameOf(n, content)
	if name == "" {
		return
	}
	id := relPath + "::" + name
	kind := KindFunction
	if isMethod {
		kind = KindMethod
	}
	fx.Nodes = append(fx.Nodes, Node{
		ID:             id,
		Label:          name,
		Kind:           kind,
		Language:       language,
		SourceFile:     relPath,
		SourceLocation: tsRange(n),
		Attrs:          map[string]any{"exported": exported},
	})
	fx.Edges = append(fx.Edges, Edge{
		Source: fileID, Target: id, Relation: RelContains, Confidence: ConfidenceExtracted,
	})
	body := n.ChildByFieldName("body")
	if body != nil {
		walkTSCalls(fx, body, content, id, declSymbols, imports)
	}
}

func emitTSClass(fx *FileExtraction, n *sitter.Node, content []byte, fileID, relPath, language string, declSymbols, imports map[string]string, exported bool) {
	name := nameOf(n, content)
	if name == "" {
		return
	}
	classID := relPath + "::" + name
	fx.Nodes = append(fx.Nodes, Node{
		ID:             classID,
		Label:          name,
		Kind:           KindClass,
		Language:       language,
		SourceFile:     relPath,
		SourceLocation: tsRange(n),
		Attrs:          map[string]any{"exported": exported},
	})
	fx.Edges = append(fx.Edges, Edge{
		Source: fileID, Target: classID, Relation: RelContains, Confidence: ConfidenceExtracted,
	})
	body := n.ChildByFieldName("body")
	if body == nil {
		return
	}
	for i := uint32(0); i < body.NamedChildCount(); i++ {
		c := body.NamedChild(int(i))
		if c.Type() != "method_definition" {
			continue
		}
		mname := nameOf(c, content)
		if mname == "" {
			continue
		}
		mid := relPath + "::" + name + "." + mname
		fx.Nodes = append(fx.Nodes, Node{
			ID:             mid,
			Label:          mname,
			Kind:           KindMethod,
			Language:       language,
			SourceFile:     relPath,
			SourceLocation: tsRange(c),
			Attrs: map[string]any{
				"receiver": name,
			},
		})
		fx.Edges = append(fx.Edges, Edge{
			Source: classID, Target: mid, Relation: RelContains, Confidence: ConfidenceExtracted,
		})
		fx.Edges = append(fx.Edges, Edge{
			Source: mid, Target: classID, Relation: RelUsesType, Confidence: ConfidenceExtracted,
			Attrs: map[string]any{"role": "receiver"},
		})
		mbody := c.ChildByFieldName("body")
		if mbody != nil {
			walkTSCalls(fx, mbody, content, mid, declSymbols, imports)
		}
	}
}

func emitTSSimpleDecl(fx *FileExtraction, n *sitter.Node, content []byte, fileID, relPath, language string, kind NodeKind, exported bool) {
	name := nameOf(n, content)
	if name == "" {
		return
	}
	id := relPath + "::" + name
	fx.Nodes = append(fx.Nodes, Node{
		ID:             id,
		Label:          name,
		Kind:           kind,
		Language:       language,
		SourceFile:     relPath,
		SourceLocation: tsRange(n),
		Attrs:          map[string]any{"exported": exported},
	})
	fx.Edges = append(fx.Edges, Edge{
		Source: fileID, Target: id, Relation: RelContains, Confidence: ConfidenceExtracted,
	})
}

// emitTSVarDecl handles `const foo = () => {}` / `const Bar = class {}`
// and similar. Treats arrow-function and class assignments as named
// function/class nodes; otherwise drops the variable.
func emitTSVarDecl(fx *FileExtraction, n *sitter.Node, content []byte, fileID, relPath, language string, declSymbols, imports map[string]string, exported bool) {
	for i := uint32(0); i < n.NamedChildCount(); i++ {
		decl := n.NamedChild(int(i))
		if decl.Type() != "variable_declarator" {
			continue
		}
		nameNode := decl.ChildByFieldName("name")
		valueNode := decl.ChildByFieldName("value")
		if nameNode == nil || valueNode == nil {
			continue
		}
		name := nameNode.Content(content)
		if name == "" {
			continue
		}
		id := relPath + "::" + name
		switch valueNode.Type() {
		case "arrow_function", "function_expression":
			fx.Nodes = append(fx.Nodes, Node{
				ID:             id,
				Label:          name,
				Kind:           KindFunction,
				Language:       language,
				SourceFile:     relPath,
				SourceLocation: tsRange(decl),
				Attrs:          map[string]any{"exported": exported, "form": "arrow"},
			})
			fx.Edges = append(fx.Edges, Edge{
				Source: fileID, Target: id, Relation: RelContains, Confidence: ConfidenceExtracted,
			})
			body := valueNode.ChildByFieldName("body")
			if body != nil {
				walkTSCalls(fx, body, content, id, declSymbols, imports)
			}
		case "class", "class_expression":
			fx.Nodes = append(fx.Nodes, Node{
				ID:             id,
				Label:          name,
				Kind:           KindClass,
				Language:       language,
				SourceFile:     relPath,
				SourceLocation: tsRange(decl),
				Attrs:          map[string]any{"exported": exported, "form": "expression"},
			})
			fx.Edges = append(fx.Edges, Edge{
				Source: fileID, Target: id, Relation: RelContains, Confidence: ConfidenceExtracted,
			})
		}
	}
}

func collectTSImports(fx *FileExtraction, fileID string, n *sitter.Node, content []byte, imports map[string]string) {
	src := n.ChildByFieldName("source")
	if src == nil {
		return
	}
	specifier := strings.Trim(src.Content(content), `"'`)
	if specifier == "" {
		return
	}

	// Scan import clause for local names. Three shapes:
	//   import x from 'mod'                  → default
	//   import { a, b as c } from 'mod'      → named
	//   import * as ns from 'mod'            → namespace
	for i := uint32(0); i < n.NamedChildCount(); i++ {
		child := n.NamedChild(int(i))
		switch child.Type() {
		case "import_clause":
			collectImportClauseNames(child, content, specifier, imports)
		}
	}

	fx.Edges = append(fx.Edges, Edge{
		Source:     fileID,
		Target:     "import:" + specifier,
		Relation:   RelImports,
		Confidence: ConfidenceExtracted,
	})
}

func collectImportClauseNames(clause *sitter.Node, content []byte, specifier string, imports map[string]string) {
	for i := uint32(0); i < clause.NamedChildCount(); i++ {
		c := clause.NamedChild(int(i))
		switch c.Type() {
		case "identifier":
			// Default import: import Foo from 'mod'
			imports[c.Content(content)] = specifier
		case "namespace_import":
			// import * as ns from 'mod'
			for j := uint32(0); j < c.NamedChildCount(); j++ {
				inner := c.NamedChild(int(j))
				if inner.Type() == "identifier" {
					imports[inner.Content(content)] = specifier
				}
			}
		case "named_imports":
			for j := uint32(0); j < c.NamedChildCount(); j++ {
				spec := c.NamedChild(int(j))
				if spec.Type() != "import_specifier" {
					continue
				}
				// Could be `name` or `name as alias`
				nameNode := spec.ChildByFieldName("name")
				aliasNode := spec.ChildByFieldName("alias")
				local := ""
				if aliasNode != nil {
					local = aliasNode.Content(content)
				} else if nameNode != nil {
					local = nameNode.Content(content)
				}
				if local != "" {
					imports[local] = specifier
				}
			}
		}
	}
}

func collectTSVarDeclSymbols(n *sitter.Node, content []byte, relPath string, declSymbols map[string]string) {
	for i := uint32(0); i < n.NamedChildCount(); i++ {
		decl := n.NamedChild(int(i))
		if decl.Type() != "variable_declarator" {
			continue
		}
		nameNode := decl.ChildByFieldName("name")
		valueNode := decl.ChildByFieldName("value")
		if nameNode == nil || valueNode == nil {
			continue
		}
		switch valueNode.Type() {
		case "arrow_function", "function_expression", "class", "class_expression":
			name := nameNode.Content(content)
			if name != "" {
				declSymbols[name] = relPath + "::" + name
			}
		}
	}
}

// walkTSCalls recurses through a body and emits calls edges. Only
// resolves: same-file function calls and direct-import member access.
// Drops everything else (ADR v1 stance).
func walkTSCalls(fx *FileExtraction, n *sitter.Node, content []byte, callerID string, declSymbols, imports map[string]string) {
	seen := map[string]bool{}
	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil {
			return
		}
		if node.Type() == "call_expression" {
			fn := node.ChildByFieldName("function")
			target := resolveTSCallTarget(fn, content, declSymbols, imports)
			if target != "" {
				key := callerID + "->" + target
				if !seen[key] {
					seen[key] = true
					line := int(node.StartPoint().Row) + 1
					fx.Edges = append(fx.Edges, Edge{
						Source:     callerID,
						Target:     target,
						Relation:   RelCalls,
						Confidence: ConfidenceExtracted,
						Attrs: map[string]any{
							"call_site": line,
						},
					})
				}
			}
		}
		for i := uint32(0); i < node.NamedChildCount(); i++ {
			walk(node.NamedChild(int(i)))
		}
	}
	walk(n)
}

func resolveTSCallTarget(fn *sitter.Node, content []byte, declSymbols, imports map[string]string) string {
	if fn == nil {
		return ""
	}
	switch fn.Type() {
	case "identifier":
		name := fn.Content(content)
		if id, ok := declSymbols[name]; ok {
			return id
		}
		// Direct named import: import { foo } from 'bar' → foo() resolves
		// to import:bar.foo
		if spec, ok := imports[name]; ok {
			return "import:" + spec + "." + name
		}
	case "member_expression":
		// x.y() — only resolve if x is a known import (namespace or default)
		obj := fn.ChildByFieldName("object")
		prop := fn.ChildByFieldName("property")
		if obj == nil || prop == nil {
			return ""
		}
		if obj.Type() == "identifier" {
			objName := obj.Content(content)
			if spec, ok := imports[objName]; ok {
				return "import:" + spec + "." + prop.Content(content)
			}
		}
	}
	return ""
}

// nameOf returns the identifier text for declarations that have a
// `name` field (function/class/interface/type_alias).
func nameOf(n *sitter.Node, content []byte) string {
	nameNode := n.ChildByFieldName("name")
	if nameNode == nil {
		return ""
	}
	return nameNode.Content(content)
}

// exportInner returns the inner declaration of an export_statement, or
// nil if it's a re-export / export-from-anchor without a body.
func exportInner(exp *sitter.Node) *sitter.Node {
	for i := uint32(0); i < exp.NamedChildCount(); i++ {
		c := exp.NamedChild(int(i))
		switch c.Type() {
		case "function_declaration", "class_declaration", "interface_declaration",
			"type_alias_declaration", "lexical_declaration", "variable_declaration":
			return c
		}
	}
	return nil
}

func tsRange(n *sitter.Node) string {
	a := int(n.StartPoint().Row) + 1
	b := int(n.EndPoint().Row) + 1
	if a == b {
		return fmt.Sprintf("L%d", a)
	}
	return fmt.Sprintf("L%d-L%d", a, b)
}
