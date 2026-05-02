## The flow to trace

{{.UserPrompt}}

## Repo

- **Slug**: `{{.RepoSlug}}`
- **Path on disk**: `{{.RepoPath}}` — use Read/Grep/Glob here for behavioral understanding.

## Graph snapshot

A deterministic knowledge graph extracted from this repo's AST lives at:

`{{.GraphPath}}`

Read this file to orient yourself. It's typically 50–500KB of JSON — too large to scan blindly. Use targeted reads:

- `Grep` it for specific function/class names mentioned in the user prompt to find their node IDs and relationships.
- `Read` selected ranges if you need surrounding context.
- For broad shape questions, the file's top-level structure is `{ nodes: [...], edges: [...], communities: {...} }` — the first ~100 lines show the schema; you can sample further from there.

Use the graph to find entry points and the call chain, then read the actual source files to confirm each step's behavior.

## Reminder

Your final assistant message MUST be a single valid JSON object matching the `Trace` schema (entry + steps). No prose. No markdown fences. No file writes — your reply IS the artifact.
