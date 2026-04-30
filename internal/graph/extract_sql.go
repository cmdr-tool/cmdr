package graph

import (
	"context"
	"path"
	"strings"

	sitter "github.com/smacker/go-tree-sitter"
	"github.com/smacker/go-tree-sitter/sql"
)

// extractSQL parses a .sql file and emits table + column nodes with
// foreign-key edges. Powers the SchemaFacet (Phase 5b).
//
// Scope: CREATE TABLE statements only. Each table becomes a node;
// each column becomes a node contained by its table. Foreign keys
// emit `foreign_key` edges from the referencing column to the
// referenced column, but only when both tables are defined in the
// same file (cross-file resolution is deferred — would require a
// global symbol table pass).
func extractSQL(relPath string, content []byte) (*FileExtraction, error) {
	parser := sitter.NewParser()
	parser.SetLanguage(sql.GetLanguage())
	tree, err := parser.ParseCtx(context.Background(), nil, content)
	if err != nil {
		return &FileExtraction{Language: "sql"}, nil
	}
	defer tree.Close()

	fx := &FileExtraction{Language: "sql"}
	fileID := relPath
	fx.Nodes = append(fx.Nodes, Node{
		ID:         fileID,
		Label:      path.Base(relPath),
		Kind:       KindFile,
		Language:   "sql",
		SourceFile: relPath,
	})

	root := tree.RootNode()

	// First pass: index table names so foreign keys defined later in
	// the file can resolve to tables defined earlier (or vice versa).
	tableIDs := map[string]string{}
	forEachStatement(root, func(stmt *sitter.Node) {
		ct := findFirstChild(stmt, "create_table")
		if ct == nil {
			return
		}
		name := sqlTableName(ct, content)
		if name != "" {
			tableIDs[name] = relPath + "::" + name
		}
	})

	// Second pass: emit nodes + edges.
	forEachStatement(root, func(stmt *sitter.Node) {
		ct := findFirstChild(stmt, "create_table")
		if ct == nil {
			return
		}
		emitSQLTable(fx, ct, content, fileID, relPath, tableIDs)
	})

	return fx, nil
}

func forEachStatement(root *sitter.Node, fn func(*sitter.Node)) {
	for i := uint32(0); i < root.NamedChildCount(); i++ {
		c := root.NamedChild(int(i))
		if c.Type() == "statement" {
			fn(c)
		}
	}
}

func findFirstChild(n *sitter.Node, kind string) *sitter.Node {
	if n == nil {
		return nil
	}
	for i := uint32(0); i < n.NamedChildCount(); i++ {
		c := n.NamedChild(int(i))
		if c.Type() == kind {
			return c
		}
	}
	return nil
}

func sqlTableName(ct *sitter.Node, content []byte) string {
	ref := findFirstChild(ct, "object_reference")
	if ref == nil {
		return ""
	}
	name := ref.ChildByFieldName("name")
	if name == nil {
		return ""
	}
	return strings.Trim(name.Content(content), `"'`+"`")
}

func emitSQLTable(fx *FileExtraction, ct *sitter.Node, content []byte, fileID, relPath string, tableIDs map[string]string) {
	tableName := sqlTableName(ct, content)
	if tableName == "" {
		return
	}
	tableID := tableIDs[tableName]
	if tableID == "" {
		return
	}
	fx.Nodes = append(fx.Nodes, Node{
		ID:             tableID,
		Label:          tableName,
		Kind:           KindTable,
		Language:       "sql",
		SourceFile:     relPath,
		SourceLocation: tsRange(ct),
	})
	fx.Edges = append(fx.Edges, Edge{
		Source: fileID, Target: tableID, Relation: RelContains, Confidence: ConfidenceExtracted,
	})

	defs := findFirstChild(ct, "column_definitions")
	if defs == nil {
		return
	}

	for i := uint32(0); i < defs.NamedChildCount(); i++ {
		c := defs.NamedChild(int(i))
		switch c.Type() {
		case "column_definition":
			emitSQLColumn(fx, c, content, tableID, tableName, relPath, tableIDs)
		case "constraints":
			emitSQLTableConstraints(fx, c, content, tableID, tableName, relPath, tableIDs)
		}
	}
}

func emitSQLColumn(fx *FileExtraction, col *sitter.Node, content []byte, tableID, tableName, relPath string, tableIDs map[string]string) {
	nameNode := col.ChildByFieldName("name")
	if nameNode == nil {
		return
	}
	colName := strings.Trim(nameNode.Content(content), `"'`+"`")
	if colName == "" {
		return
	}
	colID := tableID + "." + colName
	fx.Nodes = append(fx.Nodes, Node{
		ID:             colID,
		Label:          colName,
		Kind:           KindColumn,
		Language:       "sql",
		SourceFile:     relPath,
		SourceLocation: tsRange(col),
		Attrs: map[string]any{
			"table": tableName,
		},
	})
	fx.Edges = append(fx.Edges, Edge{
		Source: tableID, Target: colID, Relation: RelContains, Confidence: ConfidenceExtracted,
	})

	// Inline `REFERENCES otherTable(otherCol)` on a column definition.
	// Tree-sitter SQL emits these as keyword_references + object_reference
	// + identifier among the children of column_definition.
	hasRefs := false
	var refTable, refCol string
	for i := uint32(0); i < col.NamedChildCount(); i++ {
		c := col.NamedChild(int(i))
		if c.Type() == "keyword_references" {
			hasRefs = true
			continue
		}
		if hasRefs {
			if c.Type() == "object_reference" {
				if n := c.ChildByFieldName("name"); n != nil {
					refTable = strings.Trim(n.Content(content), `"'`+"`")
				}
			} else if c.Type() == "identifier" && refTable != "" {
				refCol = strings.Trim(c.Content(content), `"'`+"`")
				break
			}
		}
	}
	if refTable != "" && refCol != "" {
		if otherTableID, ok := tableIDs[refTable]; ok {
			fx.Edges = append(fx.Edges, Edge{
				Source:     colID,
				Target:     otherTableID + "." + refCol,
				Relation:   RelForeignKey,
				Confidence: ConfidenceExtracted,
			})
		}
	}
}

// emitSQLTableConstraints handles table-level constraints — namely
// FOREIGN KEY (col) REFERENCES other(col) inside the column_definitions
// constraints block.
func emitSQLTableConstraints(fx *FileExtraction, constraints *sitter.Node, content []byte, tableID, tableName, relPath string, tableIDs map[string]string) {
	for i := uint32(0); i < constraints.NamedChildCount(); i++ {
		c := constraints.NamedChild(int(i))
		if c.Type() != "constraint" {
			continue
		}
		emitSQLForeignKey(fx, c, content, tableID, relPath, tableIDs)
	}
}

func emitSQLForeignKey(fx *FileExtraction, constraint *sitter.Node, content []byte, tableID, relPath string, tableIDs map[string]string) {
	// Walk children looking for the pattern:
	//   keyword_foreign keyword_key (ordered_columns ...) keyword_references object_reference identifier
	var (
		isForeign  bool
		fromCol    string
		refTable   string
		refCol     string
	)
	for i := uint32(0); i < constraint.NamedChildCount(); i++ {
		c := constraint.NamedChild(int(i))
		switch c.Type() {
		case "keyword_foreign":
			isForeign = true
		case "ordered_columns":
			if isForeign {
				inner := findFirstChild(c, "column")
				if inner != nil {
					if n := inner.ChildByFieldName("name"); n != nil {
						fromCol = strings.Trim(n.Content(content), `"'`+"`")
					}
				}
			}
		case "object_reference":
			if isForeign {
				if n := c.ChildByFieldName("name"); n != nil {
					refTable = strings.Trim(n.Content(content), `"'`+"`")
				}
			}
		case "identifier":
			if isForeign && refTable != "" && refCol == "" {
				refCol = strings.Trim(c.Content(content), `"'`+"`")
			}
		}
	}
	if !isForeign || fromCol == "" || refTable == "" || refCol == "" {
		return
	}
	otherTableID, ok := tableIDs[refTable]
	if !ok {
		return
	}
	fx.Edges = append(fx.Edges, Edge{
		Source:     tableID + "." + fromCol,
		Target:     otherTableID + "." + refCol,
		Relation:   RelForeignKey,
		Confidence: ConfidenceExtracted,
	})
}
