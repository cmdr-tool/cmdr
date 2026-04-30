package graph

import (
	"github.com/smacker/go-tree-sitter/javascript"
)

// extractJS handles .js / .mjs / .cjs files. The JavaScript grammar
// shares the same node-type vocabulary as TypeScript for the parts we
// care about (function_declaration, class_declaration, import_statement,
// call_expression, member_expression, etc.), so we delegate to the
// shared TS walker with a JS grammar.
//
// CommonJS specifics — require()/module.exports — are deliberately
// dropped for v1: a `require('foo')` call would resolve as a call to
// `import:foo.require` which isn't useful. The ADR's v1 stance favors
// undercounting over wrong arrows. ESM imports work as in TS.
func extractJS(relPath string, content []byte) (*FileExtraction, error) {
	return extractWithTSGrammar(relPath, content, javascript.GetLanguage(), "js")
}
