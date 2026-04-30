package graph

import (
	"strings"

	sitter "github.com/tree-sitter/go-tree-sitter"
)

// walkMongoPatterns scans a TS/JS AST for MongoDB usage patterns and
// emits collection nodes + accesses edges. Called from walkTSDecls
// after the structural decl walk so it sees the same tree without
// reparsing.
//
// Captures two patterns:
//   - any expression ending in `.collection('NAME')` — including
//     fastify-mongo's `fastify.mongo.db.collection('users')` and
//     raw-driver `db.collection('users')` shapes.
//   - $lookup pipelines inside aggregations: { $lookup: { from: 'NAME', ... } }
//
// For each detected collection name it emits one node (deduplicated by
// id) and an `accesses` edge from the enclosing function/method to
// that collection. v1 doesn't model collection→collection joins
// explicitly — both endpoints of a $lookup show up as collections the
// enclosing function accesses, which is enough to see the structural
// relationship in the graph.
func walkMongoPatterns(fx *FileExtraction, root *sitter.Node, content []byte, fileID, relPath, language string, declSymbols map[string]string) {
	emittedCollections := map[string]bool{}
	emittedEdges := map[string]bool{}

	emitCollection := func(name string) string {
		id := "mongo:collection:" + name
		if !emittedCollections[id] {
			emittedCollections[id] = true
			fx.Nodes = append(fx.Nodes, Node{
				ID:       id,
				Label:    name,
				Kind:     KindCollection,
				Language: language,
				Attrs: map[string]any{
					"db": "mongo",
				},
			})
		}
		return id
	}

	emitAccess := func(callerID, collectionID string, line int) {
		key := callerID + "|" + collectionID
		if emittedEdges[key] {
			return
		}
		emittedEdges[key] = true
		fx.Edges = append(fx.Edges, Edge{
			Source: callerID, Target: collectionID,
			Relation: RelAccesses, Confidence: ConfidenceExtracted,
			Attrs: map[string]any{"call_site": line},
		})
	}

	// Walk the tree, tracking the enclosing function/method/class so
	// access edges are attributed to the right caller.
	var walk func(n *sitter.Node, callerID string)
	walk = func(n *sitter.Node, callerID string) {
		if n == nil {
			return
		}
		// Update caller context when entering a function-shaped node.
		newCaller := callerID
		switch n.Kind() {
		case "function_declaration":
			if name := nameOf(n, content); name != "" {
				newCaller = relPath + "::" + name
			}
		case "method_definition":
			// Method's caller id includes the enclosing class name.
			// Find the nearest class ancestor by walking up via the field
			// owner — but tree-sitter's parent navigation is simpler than
			// that: we'll resolve once, on demand.
			if cid := mongoMethodCallerID(n, content, relPath); cid != "" {
				newCaller = cid
			}
		case "variable_declarator":
			// const foo = () => {} — bind caller to foo for the body
			nameNode := n.ChildByFieldName("name")
			valueNode := n.ChildByFieldName("value")
			if nameNode != nil && valueNode != nil &&
				(valueNode.Kind() == "arrow_function" || valueNode.Kind() == "function_expression") {
				name := nameNode.Utf8Text(content)
				if name != "" {
					newCaller = relPath + "::" + name
				}
			}
		}

		// Pattern 1: `.collection('NAME')` calls.
		if n.Kind() == "call_expression" {
			if name := mongoCollectionCallName(n, content); name != "" && newCaller != "" {
				cid := emitCollection(name)
				line := int(n.StartPosition().Row) + 1
				emitAccess(newCaller, cid, line)
			}
		}

		// Pattern 2: $lookup pair in an object literal.
		if n.Kind() == "pair" {
			if name := mongoLookupFromName(n, content); name != "" && newCaller != "" {
				cid := emitCollection(name)
				line := int(n.StartPosition().Row) + 1
				emitAccess(newCaller, cid, line)
			}
		}

		for i := uint(0); i < n.NamedChildCount(); i++ {
			walk(n.NamedChild(i), newCaller)
		}
	}

	// Start with empty caller. File-level collection accesses (outside any
	// function) get attributed to the file itself — useful for things like
	// top-level connection setup.
	walk(root, fileID)

	_ = declSymbols // reserved for future use; declSymbols would help disambiguate `.collection()` calls on user-defined types
}

// mongoCollectionCallName returns the literal name passed to a
// `.collection('NAME')` call, or "" if this isn't that pattern.
// Matches any expression of the form `<anything>.collection('LIT')`,
// covering fastify.mongo.db.collection('x'), db.collection('x'),
// this.dbs.foo.collection('x'), etc.
func mongoCollectionCallName(call *sitter.Node, content []byte) string {
	fn := call.ChildByFieldName("function")
	if fn == nil || fn.Kind() != "member_expression" {
		return ""
	}
	prop := fn.ChildByFieldName("property")
	if prop == nil || prop.Utf8Text(content) != "collection" {
		return ""
	}
	args := call.ChildByFieldName("arguments")
	if args == nil || args.NamedChildCount() == 0 {
		return ""
	}
	first := args.NamedChild(0)
	return mongoStringLiteralValue(first, content)
}

// mongoLookupFromName extracts the `from` collection name from a
// `$lookup: { from: 'NAME', ... }` pair, or "" if this pair isn't
// a $lookup or doesn't have a literal `from`.
func mongoLookupFromName(pair *sitter.Node, content []byte) string {
	key := pair.ChildByFieldName("key")
	if key == nil {
		return ""
	}
	keyText := strings.Trim(key.Utf8Text(content), `"'`)
	if keyText != "$lookup" {
		return ""
	}
	value := pair.ChildByFieldName("value")
	if value == nil || value.Kind() != "object" {
		return ""
	}
	// Walk the inner object's pairs for a `from:` key
	for i := uint(0); i < value.NamedChildCount(); i++ {
		inner := value.NamedChild(i)
		if inner.Kind() != "pair" {
			continue
		}
		ik := inner.ChildByFieldName("key")
		if ik == nil {
			continue
		}
		if strings.Trim(ik.Utf8Text(content), `"'`) != "from" {
			continue
		}
		iv := inner.ChildByFieldName("value")
		return mongoStringLiteralValue(iv, content)
	}
	return ""
}

// mongoStringLiteralValue returns the unquoted value of a TS/JS string
// literal node, or "" if the node isn't a literal.
func mongoStringLiteralValue(n *sitter.Node, content []byte) string {
	if n == nil {
		return ""
	}
	switch n.Kind() {
	case "string":
		// In tree-sitter-typescript, a string node has children
		// '"', string_fragment, '"'. The named child is the fragment.
		if n.NamedChildCount() > 0 {
			frag := n.NamedChild(0)
			if frag.Kind() == "string_fragment" {
				return frag.Utf8Text(content)
			}
		}
		// Fallback: trim quotes from the full text.
		return strings.Trim(n.Utf8Text(content), `"'`+"`")
	case "template_string":
		// Only resolve if it has no interpolation (no template_substitution
		// children) — otherwise the value isn't literal.
		hasSub := false
		for i := uint(0); i < n.NamedChildCount(); i++ {
			c := n.NamedChild(i)
			if c.Kind() == "template_substitution" {
				hasSub = true
				break
			}
		}
		if hasSub {
			return ""
		}
		return strings.Trim(n.Utf8Text(content), "`")
	}
	return ""
}

// mongoMethodCallerID returns the caller id for a method_definition,
// including its enclosing class name. Walks up via Parent() to find
// the class_declaration; if not found, falls back to the bare name.
func mongoMethodCallerID(method *sitter.Node, content []byte, relPath string) string {
	mname := nameOf(method, content)
	if mname == "" {
		return ""
	}
	// method_definition's parent is class_body; that's parent is class_declaration
	cb := method.Parent()
	if cb == nil {
		return relPath + "::" + mname
	}
	cls := cb.Parent()
	if cls == nil || cls.Kind() != "class_declaration" {
		return relPath + "::" + mname
	}
	cname := nameOf(cls, content)
	if cname == "" {
		return relPath + "::" + mname
	}
	return relPath + "::" + cname + "." + mname
}
