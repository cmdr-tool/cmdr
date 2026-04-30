package graph

import (
	"path"

	sitter "github.com/tree-sitter/go-tree-sitter"
	tree_sitter_python "github.com/tree-sitter/tree-sitter-python/bindings/go"
)

// extractPython parses a .py file and emits the same node/edge shape
// as the Go and TS extractors. Mirrors the TS structure but with
// Python's grammar conventions: function_definition / class_definition
// instead of function_declaration / class_declaration, and
// import_from_statement for `from X import Y` patterns.
func extractPython(relPath string, content []byte) (*FileExtraction, error) {
	parser := sitter.NewParser()
	defer parser.Close()
	if err := parser.SetLanguage(sitter.NewLanguage(tree_sitter_python.Language())); err != nil {
		return &FileExtraction{Language: "py"}, nil
	}
	tree := parser.Parse(content, nil)
	if tree == nil {
		return &FileExtraction{Language: "py"}, nil
	}
	defer tree.Close()

	fx := &FileExtraction{Language: "py"}
	fileID := relPath
	fx.Nodes = append(fx.Nodes, Node{
		ID:         fileID,
		Label:      path.Base(relPath),
		Kind:       KindFile,
		Language:   "py",
		SourceFile: relPath,
	})

	root := tree.RootNode()
	imports := map[string]string{} // local name → module path
	declSymbols := map[string]string{}

	// First pass: collect imports + top-level decl names.
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		switch c.Kind() {
		case "import_statement":
			collectPyImports(fx, fileID, c, content, imports)
		case "import_from_statement":
			collectPyFromImports(fx, fileID, c, content, imports)
		case "function_definition", "class_definition":
			if name := nameOf(c, content); name != "" {
				declSymbols[name] = relPath + "::" + name
			}
		case "decorated_definition":
			if inner := pyDecoratedInner(c); inner != nil {
				if name := nameOf(inner, content); name != "" {
					declSymbols[name] = relPath + "::" + name
				}
			}
		}
	}

	// Second pass: emit nodes + edges.
	for i := uint(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(i)
		emitPyDecl(fx, c, content, fileID, relPath, declSymbols, imports)
	}

	return fx, nil
}

func emitPyDecl(fx *FileExtraction, n *sitter.Node, content []byte, fileID, relPath string, declSymbols, imports map[string]string) {
	switch n.Kind() {
	case "decorated_definition":
		if inner := pyDecoratedInner(n); inner != nil {
			emitPyDecl(fx, inner, content, fileID, relPath, declSymbols, imports)
		}
	case "function_definition":
		emitPyFunction(fx, n, content, fileID, relPath, declSymbols, imports, false)
	case "class_definition":
		emitPyClass(fx, n, content, fileID, relPath, declSymbols, imports)
	}
}

func emitPyFunction(fx *FileExtraction, n *sitter.Node, content []byte, fileID, relPath string, declSymbols, imports map[string]string, isMethod bool) {
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
		Language:       "py",
		SourceFile:     relPath,
		SourceLocation: tsRange(n),
	})
	fx.Edges = append(fx.Edges, Edge{
		Source: fileID, Target: id, Relation: RelContains, Confidence: ConfidenceExtracted,
	})
	body := n.ChildByFieldName("body")
	if body != nil {
		walkPyCalls(fx, body, content, id, declSymbols, imports)
	}
}

func emitPyClass(fx *FileExtraction, n *sitter.Node, content []byte, fileID, relPath string, declSymbols, imports map[string]string) {
	name := nameOf(n, content)
	if name == "" {
		return
	}
	classID := relPath + "::" + name
	fx.Nodes = append(fx.Nodes, Node{
		ID:             classID,
		Label:          name,
		Kind:           KindClass,
		Language:       "py",
		SourceFile:     relPath,
		SourceLocation: tsRange(n),
	})
	fx.Edges = append(fx.Edges, Edge{
		Source: fileID, Target: classID, Relation: RelContains, Confidence: ConfidenceExtracted,
	})
	body := n.ChildByFieldName("body")
	if body == nil {
		return
	}
	for i := uint(0); i < body.NamedChildCount(); i++ {
		c := body.NamedChild(i)
		// Methods can be plain function_definitions or wrapped in
		// decorated_definition for @classmethod / @staticmethod / etc.
		fn := c
		if c.Kind() == "decorated_definition" {
			fn = pyDecoratedInner(c)
		}
		if fn == nil || fn.Kind() != "function_definition" {
			continue
		}
		mname := nameOf(fn, content)
		if mname == "" {
			continue
		}
		mid := relPath + "::" + name + "." + mname
		fx.Nodes = append(fx.Nodes, Node{
			ID:             mid,
			Label:          mname,
			Kind:           KindMethod,
			Language:       "py",
			SourceFile:     relPath,
			SourceLocation: tsRange(fn),
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
		mbody := fn.ChildByFieldName("body")
		if mbody != nil {
			walkPyCalls(fx, mbody, content, mid, declSymbols, imports)
		}
	}
}

// collectPyImports handles `import X` and `import X as Y`. The dotted
// name (e.g. `foo.bar`) is the module path; the alias or last
// component becomes the local binding.
func collectPyImports(fx *FileExtraction, fileID string, n *sitter.Node, content []byte, imports map[string]string) {
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		switch c.Kind() {
		case "dotted_name":
			modulePath := c.Utf8Text(content)
			// Local binding: last component of the dotted name
			local := modulePath
			if idx := lastDot(modulePath); idx >= 0 {
				local = modulePath[idx+1:]
			}
			imports[local] = modulePath
			fx.Edges = append(fx.Edges, Edge{
				Source: fileID, Target: "import:" + modulePath,
				Relation: RelImports, Confidence: ConfidenceExtracted,
			})
		case "aliased_import":
			nameNode := c.ChildByFieldName("name")
			aliasNode := c.ChildByFieldName("alias")
			if nameNode == nil || aliasNode == nil {
				continue
			}
			modulePath := nameNode.Utf8Text(content)
			alias := aliasNode.Utf8Text(content)
			imports[alias] = modulePath
			fx.Edges = append(fx.Edges, Edge{
				Source: fileID, Target: "import:" + modulePath,
				Relation: RelImports, Confidence: ConfidenceExtracted,
				Attrs: map[string]any{"alias": alias},
			})
		}
	}
}

// collectPyFromImports handles `from X import Y` and `from X import Y as Z`.
// Each imported name becomes a local binding pointing at "X.Y".
func collectPyFromImports(fx *FileExtraction, fileID string, n *sitter.Node, content []byte, imports map[string]string) {
	module := n.ChildByFieldName("module_name")
	if module == nil {
		return
	}
	modulePath := module.Utf8Text(content)

	fx.Edges = append(fx.Edges, Edge{
		Source: fileID, Target: "import:" + modulePath,
		Relation: RelImports, Confidence: ConfidenceExtracted,
	})

	// Walk children for the imported name list.
	for i := uint(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(i)
		if c == module {
			continue
		}
		switch c.Kind() {
		case "dotted_name":
			name := c.Utf8Text(content)
			imports[name] = modulePath
		case "aliased_import":
			nameNode := c.ChildByFieldName("name")
			aliasNode := c.ChildByFieldName("alias")
			if nameNode == nil || aliasNode == nil {
				continue
			}
			imports[aliasNode.Utf8Text(content)] = modulePath
		}
	}
}

// walkPyCalls recurses through a body and emits calls edges.
// Same v1 conservative resolution as TS: same-file functions and
// directly-imported names; drops everything else.
func walkPyCalls(fx *FileExtraction, n *sitter.Node, content []byte, callerID string, declSymbols, imports map[string]string) {
	seen := map[string]bool{}
	var walk func(*sitter.Node)
	walk = func(node *sitter.Node) {
		if node == nil {
			return
		}
		if node.Kind() == "call" {
			fn := node.ChildByFieldName("function")
			target := resolvePyCallTarget(fn, content, declSymbols, imports)
			if target != "" {
				key := callerID + "->" + target
				if !seen[key] {
					seen[key] = true
					fx.Edges = append(fx.Edges, Edge{
						Source: callerID, Target: target,
						Relation: RelCalls, Confidence: ConfidenceExtracted,
						Attrs: map[string]any{
							"call_site": int(node.StartPosition().Row) + 1,
						},
					})
				}
			}
		}
		for i := uint(0); i < node.NamedChildCount(); i++ {
			walk(node.NamedChild(i))
		}
	}
	walk(n)
}

func resolvePyCallTarget(fn *sitter.Node, content []byte, declSymbols, imports map[string]string) string {
	if fn == nil {
		return ""
	}
	switch fn.Kind() {
	case "identifier":
		name := fn.Utf8Text(content)
		if id, ok := declSymbols[name]; ok {
			return id
		}
		if mod, ok := imports[name]; ok {
			return "import:" + mod + "." + name
		}
	case "attribute":
		// obj.method() — only resolve if obj is a known import alias
		obj := fn.ChildByFieldName("object")
		attr := fn.ChildByFieldName("attribute")
		if obj == nil || attr == nil {
			return ""
		}
		if obj.Kind() == "identifier" {
			objName := obj.Utf8Text(content)
			if mod, ok := imports[objName]; ok {
				return "import:" + mod + "." + attr.Utf8Text(content)
			}
		}
	}
	return ""
}

// pyDecoratedInner returns the function_definition or class_definition
// wrapped by a decorated_definition node. Returns nil if it's something
// else (rare).
func pyDecoratedInner(dec *sitter.Node) *sitter.Node {
	for i := uint(0); i < dec.NamedChildCount(); i++ {
		c := dec.NamedChild(i)
		if c.Kind() == "function_definition" || c.Kind() == "class_definition" {
			return c
		}
	}
	return nil
}

// lastDot returns the index of the last '.' in s, or -1 if none.
// Avoid pulling in strings just for a single LastIndex call.
func lastDot(s string) int {
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '.' {
			return i
		}
	}
	return -1
}
