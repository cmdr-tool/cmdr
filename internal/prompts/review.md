You are reviewing a commit for **codebase health**. The system prompt defines the review philosophy and strictness. Your job here is to review the supplied commit using the materials below and return structured findings.

Do not review primarily for functional correctness, security, or feature completeness unless a problem materially affects codebase health, maintainability, or architectural integrity.

## Commit
- Repository: {{.RepoName}}
- SHA: {{.SHA}}
- Author: {{.Author}}
- Date: {{.Date}}
- Message: {{.Message}}

## Diff
```diff
{{.Diff}}
```

## Review Scope

Review the diff in context. Read the surrounding code in touched files and, when necessary, adjacent modules to understand whether the change belongs in that layer, file, and pattern family.

If `docs/PATTERNS*.md` files exist and are relevant to the touched code, read them and use them as project context.

Focus findings on these areas:

1. **Boundary / Architecture** — whether responsibilities remain in the correct layer and dependency flow stays coherent
2. **Cohesion / API Shape** — whether functions, objects, and module APIs stay understandable and cohesive
3. **Readability / Local Reasoning** — whether the code is easy to understand in place via clear naming, straightforward control flow, visible decisions, and low cognitive overhead
4. **Organizational Fit** — whether code belongs in the current file/module and whether new files or abstractions are justified
5. **Consistency / Local Pattern Fit** — whether the change aligns with established patterns in nearby code and project docs
6. **Side Effects / Imperative Shell** — whether side effects stay visible at the edges and pure transformation logic remains separable
7. **DRY / Abstraction Fit** — whether duplication indicates a real missing abstraction rather than harmless repetition

Only report meaningful findings. Do not narrate what the code does or summarize the change. Avoid surfacing unrelated pre-existing issues unless the diff worsens them or should clearly have aligned with nearby code. Include every materially distinct, high-signal issue you can support from the diff and surrounding context, but do not pad the review with weak findings.
{{if .CommitNote}}

## Reviewer's Note
The reviewer has provided the following general note about this commit:
> {{.CommitNote}}
{{end}}
{{if .Annotations}}

## Reviewer's Annotations
The reviewer has flagged specific areas of concern (line numbers are 1-indexed into the diff above).
{{range .Annotations}}

### Lines {{.LineStart}}–{{.LineEnd}}
```
{{.Context}}
```
> {{.Comment}}
{{end}}
{{end}}
{{if or .Annotations .CommitNote}}

## Notes to Address
{{if .CommitNote}}- Address the reviewer's general note about this commit{{end}}
{{if .Annotations}}- Address each line annotation directly{{end}}
- If a concern is valid, incorporate it into the finding and plan
- If a concern is not valid given the project's conventions, say so plainly
{{end}}

## Output Format

For each finding, use:

```
### [N. Category] Finding Title
`file/path` · lines X–Y

**Issue:** One-sentence description of what's wrong
**Severity:** must-fix | should-fix | optional

\`\`\`language
// the relevant code snippet from the diff or current file
// include enough surrounding context to understand the problem
// for changes, show the new code as it appears after the commit
\`\`\`

**Why it matters:** How this degrades the codebase over time

**Plan:**
1. [Step]: what to do and where (file:method/function)
2. [Step]: ...
```

### Code snippet guidelines

Every finding **must** include the relevant code snippet inline. The reader should be able to understand the finding without switching to an editor. Follow these rules:

- Show the **actual code** that the finding refers to — not a paraphrase or pseudocode
- Include enough **surrounding context** (3–5 lines before/after) to understand the code's role
- For diff-related findings, show the code **as it appears after the commit** (the new version)
- If the finding is about a missing pattern or structural issue, show the code that should change
- Use the correct language identifier for syntax highlighting (e.g. `js`, `go`, `ts`, `svelte`)
- Keep snippets focused — 5–20 lines is ideal, don't dump entire functions

### Other format notes

Use N from the seven scope areas above. `N` identifies the scope area, not the priority rank. `Severity` may be omitted for optional findings. `Confidence` may be added when a finding involves judgment calls. Order findings by severity first (`must-fix`, then `should-fix`, then `optional`), breaking ties by architectural impact and breadth.

The plan should be a sequence of steps that a refactoring agent can follow. Each step should point at a **location and intention** — e.g. "Extract the media resolution block from `derive()` in `createSocialPost.js` into a new `productService.resolveVariantMedia()` method". Do not include code snippets or exact implementations in the plan — the agent will figure that out. Keep steps scoped to the finding; do not combine multiple findings into one plan.

Skip scope areas with no findings. Do not omit a real finding merely to keep the review short, and do not merge separate issues just to reduce the count. If the change is clean, say so in one sentence — do not pad with praise or generic observations.
