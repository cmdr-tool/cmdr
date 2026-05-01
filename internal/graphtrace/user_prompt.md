## Inputs

**Repo context** (markdown describing the repo's architecture, entry points, and notable flows — written by the human owner):

{{.RepoContext}}

**Graph snapshot**: A deterministic knowledge graph extracted from the AST is at `{{.GraphPath}}`. Nodes are files/modules/functions/methods/classes. Edges are imports/calls/contains/extends. Use it as a *map* to orient yourself — read it (or grep it) to find function names and their relationships, then read the actual code for behavioral understanding.

**Repo code**: The repository itself is at `{{.RepoPath}}`. Use Read and Grep to inspect specific files when you need to understand what code actually does.

**Trace guidance** (optional user-supplied prompt for this run):
{{if .UserGuidance}}{{.UserGuidance}}{{else}}(none provided — choose the most important top-level flows from the repo context, graph, and code.){{end}}

**Output target**: Use the Write tool to save the final JSON result to this exact path (note the `.tmp` suffix — cmdr validates the file then atomically promotes it to `traces.json`):

`{{.OutputPath}}`

Write the JSON directly to that path — do **not** include the JSON in your text response. After the file is written, respond with a brief confirmation (e.g. "done — wrote N traces") and stop. Do not stream the JSON to me.

Now produce the data flow traces, following your system instructions for scope, granularity, provenance, and output format.
