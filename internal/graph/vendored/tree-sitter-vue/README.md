# tree-sitter-vue (vendored)

Vendored Vue grammar from
[tree-sitter-grammars/tree-sitter-vue](https://github.com/tree-sitter-grammars/tree-sitter-vue).

## Why vendored

Upstream ships C, Python, and Rust bindings but not Go. We add a thin
Go binding under `bindings/go/binding.go` that follows the same pattern
as official tree-sitter Go bindings.

## Provenance

Source files copied from the `main` branch (commit captured in pseudo-
version `v0.0.0-20260124095733-ce8011a414fd`). Files vendored:

- `src/parser.c` — the generated parser (~150KB)
- `src/scanner.c` — the external scanner for Vue's structural tokens
- `src/tag.h` — Vue tag definitions used by the scanner
- `src/tree_sitter/parser.h` — tree-sitter parser API header
- `src/tree_sitter/alloc.h`, `array.h` — tree-sitter shared utilities

## Updating

To pull a newer version of the grammar:

1. `go get github.com/tree-sitter-grammars/tree-sitter-vue@latest`
2. Copy the `src/` directory contents from the module cache into this
   directory, replacing the existing files.
3. Run `go test ./internal/graph/...` to confirm nothing broke.
4. Drop the temporary go.mod entry: `go mod edit -droprequire=github.com/tree-sitter-grammars/tree-sitter-vue`.

The `bindings/go/binding.go` file is hand-maintained and stays put
across grammar updates unless the C API surface changes (rare).
