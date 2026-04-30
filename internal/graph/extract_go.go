package graph

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path"
	"strconv"
	"strings"
)

// extractGo parses a Go source file at relPath (relative to the repo
// root) and returns the nodes and edges it represents. The file's
// content is passed in directly so callers can hash it for caching
// upstream.
//
// Emits:
//   - one file node per file
//   - one function/method/type/interface node per top-level decl
//   - "contains" edges from file → declarations
//   - "imports" edges from file → imported package paths
//   - "calls" edges (only when target is unambiguously resolvable
//     within the same file or via a directly imported package)
//   - "uses_type" edges from functions to named types they reference
func extractGo(relPath string, content []byte) (*FileExtraction, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, relPath, content, parser.SkipObjectResolution)
	if err != nil {
		// Skip on parse error — better to drop one file than fail the whole graph.
		return &FileExtraction{Language: "go"}, nil
	}

	fx := &FileExtraction{Language: "go"}

	fileID := relPath
	fx.Nodes = append(fx.Nodes, Node{
		ID:         fileID,
		Label:      path.Base(relPath),
		Kind:       KindFile,
		Language:   "go",
		SourceFile: relPath,
		Attrs: map[string]any{
			"package": f.Name.Name,
		},
	})

	// Map of imported package paths to local names (alias or basename).
	// Used to resolve `pkg.Foo()` style calls into a stable target id.
	imports := map[string]string{} // localName -> importPath
	for _, imp := range f.Imports {
		p, err := strconv.Unquote(imp.Path.Value)
		if err != nil {
			continue
		}
		local := path.Base(p)
		if imp.Name != nil {
			local = imp.Name.Name
		}
		imports[local] = p

		fx.Edges = append(fx.Edges, Edge{
			Source:     fileID,
			Target:     "import:" + p,
			Relation:   RelImports,
			Confidence: ConfidenceExtracted,
			Attrs: map[string]any{
				"local": local,
			},
		})
	}

	// First pass: collect declared symbols so we can resolve same-file calls.
	declSymbols := map[string]string{} // simple name -> full node id
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			id := goSymbolID(relPath, d)
			declSymbols[d.Name.Name] = id
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				if ts, ok := spec.(*ast.TypeSpec); ok {
					id := relPath + "::" + ts.Name.Name
					declSymbols[ts.Name.Name] = id
				}
			}
		}
	}

	// Second pass: emit nodes + edges.
	for _, decl := range f.Decls {
		switch d := decl.(type) {
		case *ast.FuncDecl:
			emitFuncDecl(fx, fset, fileID, relPath, d, declSymbols, imports)
		case *ast.GenDecl:
			emitGenDecl(fx, fset, fileID, relPath, d)
		}
	}

	return fx, nil
}

// goSymbolID renders the canonical node id for a function or method.
// Format: <relpath>::Receiver.Name for methods, <relpath>::Name for funcs.
func goSymbolID(relPath string, fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		recv := receiverTypeName(fn.Recv.List[0].Type)
		recv = strings.TrimPrefix(recv, "*")
		if recv != "" {
			return fmt.Sprintf("%s::%s.%s", relPath, recv, fn.Name.Name)
		}
	}
	return fmt.Sprintf("%s::%s", relPath, fn.Name.Name)
}

func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.Ident:
		return t.Name
	case *ast.StarExpr:
		return "*" + receiverTypeName(t.X)
	case *ast.IndexExpr:
		return receiverTypeName(t.X)
	case *ast.IndexListExpr:
		return receiverTypeName(t.X)
	}
	return ""
}

func emitFuncDecl(fx *FileExtraction, fset *token.FileSet, fileID, relPath string, fn *ast.FuncDecl, declSymbols, imports map[string]string) {
	id := goSymbolID(relPath, fn)
	kind := KindFunction
	receiver := ""
	if fn.Recv != nil && len(fn.Recv.List) > 0 {
		kind = KindMethod
		receiver = receiverTypeName(fn.Recv.List[0].Type)
	}

	exported := fn.Name.IsExported()

	fx.Nodes = append(fx.Nodes, Node{
		ID:             id,
		Label:          fn.Name.Name,
		Kind:           kind,
		Language:       "go",
		SourceFile:     relPath,
		SourceLocation: rangeFor(fset, fn.Pos(), fn.End()),
		Attrs: map[string]any{
			"receiver": receiver,
			"exported": exported,
		},
	})
	fx.Edges = append(fx.Edges, Edge{
		Source:     fileID,
		Target:     id,
		Relation:   RelContains,
		Confidence: ConfidenceExtracted,
	})

	// We used to emit a uses_type edge from method → receiver type, but
	// it duplicates the contains edge from receiver → method (every method
	// uses its receiver type — tautological). Dropping it cleans up the
	// graph without losing signal; uses_type body-walk below still picks
	// up references to other types.

	if fn.Body == nil {
		return
	}

	// Walk the body to find calls and named-type references.
	seen := map[string]bool{}
	ast.Inspect(fn.Body, func(n ast.Node) bool {
		switch e := n.(type) {
		case *ast.CallExpr:
			target := resolveCallTarget(e.Fun, declSymbols, imports)
			if target == "" {
				return true
			}
			edgeKey := id + "->" + target
			if seen[edgeKey] {
				return true
			}
			seen[edgeKey] = true
			fx.Edges = append(fx.Edges, Edge{
				Source:     id,
				Target:     target,
				Relation:   RelCalls,
				Confidence: ConfidenceExtracted,
				Attrs: map[string]any{
					"call_site": fset.Position(e.Pos()).Line,
				},
			})
		case *ast.Ident:
			// Reference to a named type declared in this file.
			if target, ok := declSymbols[e.Name]; ok && target != id {
				// Filter out self-references and same-file func references already
				// captured by RelCalls; only emit uses_type for type-shaped targets.
				if !strings.Contains(target, "::") {
					return true
				}
				if isTypeNodeID(target, declSymbols) {
					edgeKey := id + "uses_type" + target
					if !seen[edgeKey] {
						seen[edgeKey] = true
						fx.Edges = append(fx.Edges, Edge{
							Source:     id,
							Target:     target,
							Relation:   RelUsesType,
							Confidence: ConfidenceExtracted,
						})
					}
				}
			}
		}
		return true
	})
}

// isTypeNodeID reports whether id refers to a type-shaped declaration
// in the same file (no '.' in the symbol part means it's not a method).
func isTypeNodeID(id string, declSymbols map[string]string) bool {
	for name, sym := range declSymbols {
		if sym == id {
			return !strings.Contains(name, ".") && !looksLikeFunc(id)
		}
	}
	return false
}

// looksLikeFunc returns true when the symbol part has a method-style
// "Receiver.Name" shape — those aren't type names.
func looksLikeFunc(id string) bool {
	parts := strings.SplitN(id, "::", 2)
	if len(parts) != 2 {
		return false
	}
	return strings.Contains(parts[1], ".")
}

// resolveCallTarget tries to map a call expression's function back to
// a stable node ID. Returns "" when the target is ambiguous — per the
// ADR's v1 stance: "drop call edges that can't be unambiguously
// resolved within-file or via direct import."
func resolveCallTarget(fun ast.Expr, declSymbols, imports map[string]string) string {
	switch f := fun.(type) {
	case *ast.Ident:
		// Same-file function call.
		if id, ok := declSymbols[f.Name]; ok {
			return id
		}
	case *ast.SelectorExpr:
		// pkg.Foo() — only resolve if pkg is a known import.
		if x, ok := f.X.(*ast.Ident); ok {
			if importPath, ok := imports[x.Name]; ok {
				return "import:" + importPath + "." + f.Sel.Name
			}
		}
	}
	return ""
}

func emitGenDecl(fx *FileExtraction, fset *token.FileSet, fileID, relPath string, d *ast.GenDecl) {
	for _, spec := range d.Specs {
		ts, ok := spec.(*ast.TypeSpec)
		if !ok {
			continue
		}
		id := relPath + "::" + ts.Name.Name
		kind := KindType
		switch ts.Type.(type) {
		case *ast.InterfaceType:
			kind = KindInterface
		case *ast.StructType:
			kind = KindClass
		}
		fx.Nodes = append(fx.Nodes, Node{
			ID:             id,
			Label:          ts.Name.Name,
			Kind:           kind,
			Language:       "go",
			SourceFile:     relPath,
			SourceLocation: rangeFor(fset, ts.Pos(), ts.End()),
			Attrs: map[string]any{
				"exported": ts.Name.IsExported(),
			},
		})
		fx.Edges = append(fx.Edges, Edge{
			Source:     fileID,
			Target:     id,
			Relation:   RelContains,
			Confidence: ConfidenceExtracted,
		})
	}
}

func rangeFor(fset *token.FileSet, start, end token.Pos) string {
	a := fset.Position(start)
	b := fset.Position(end)
	if a.Line == b.Line {
		return fmt.Sprintf("L%d", a.Line)
	}
	return fmt.Sprintf("L%d-L%d", a.Line, b.Line)
}
