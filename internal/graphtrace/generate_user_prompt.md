## The flow to trace

{{.UserPrompt}}

## Repo

- **Slug**: `{{.RepoSlug}}`
- **Path on disk**: `{{.RepoPath}}` — use Read/Grep/Glob here for behavioral understanding.

## Graph snapshot

A deterministic knowledge graph extracted from this repo's AST is below. Nodes are files/modules/functions/methods/classes. Edges are imports/calls/contains/extends. Use it as a map: find the entry point for the flow above, then walk through the graph to follow the call chain — confirm each step against the actual source.

```json
{{.GraphJSON}}
```

## Reminder

Your final assistant message MUST be a single valid JSON object matching the `Trace` schema (entry + steps). No prose. No markdown fences. No file writes — your reply IS the artifact.
