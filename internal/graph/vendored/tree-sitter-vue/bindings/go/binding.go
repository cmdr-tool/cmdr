// Package tree_sitter_vue provides a Go binding for the
// tree-sitter-vue grammar. The grammar source under ../../src is
// vendored from github.com/tree-sitter-grammars/tree-sitter-vue
// because the upstream repo doesn't ship a Go binding (it has C,
// Python, and Rust bindings only). See ../../README.md for
// provenance and update procedure.
package tree_sitter_vue

// #cgo CPPFLAGS: -I../../src
// #cgo CFLAGS: -std=c11 -fPIC
// #include "../../src/parser.c"
// #include "../../src/scanner.c"
import "C"

import "unsafe"

// Language returns the tree-sitter Language for Vue, ready to be
// wrapped with sitter.NewLanguage() from the official Go bindings.
func Language() unsafe.Pointer {
	return unsafe.Pointer(C.tree_sitter_vue())
}
