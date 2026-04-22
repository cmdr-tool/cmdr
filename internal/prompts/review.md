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
3. **Organizational Fit** — whether code belongs in the current file/module and whether new files or abstractions are justified
4. **Consistency / Local Pattern Fit** — whether the change aligns with established patterns in nearby code and project docs
5. **Side Effects / Imperative Shell** — whether side effects stay visible at the edges and pure transformation logic remains separable
6. **DRY / Abstraction Fit** — whether duplication indicates a real missing abstraction rather than harmless repetition

Only report meaningful findings. Do not narrate what the code does or summarize the change. Avoid surfacing unrelated pre-existing issues unless the diff worsens them or should clearly have aligned with nearby code.
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
**Lines:** X–Y (diff), `file/path`
**Severity:** must-fix | should-fix | optional
**Confidence:** high | medium | low
**Issue:** One-sentence description of what's wrong
**Why it matters:** How this degrades the codebase over time
**Plan:**
1. [Step]: what to do and where (file:method/function), not how to write the code
2. [Step]: ...
```

Use N from the six scope areas above. `Severity` and `Confidence` may be omitted unless the system prompt requires them.

The plan should be a sequence of steps that a refactoring agent can follow. Each step should point at a **location and intention** — e.g. "Extract the media resolution block from `derive()` in `createSocialPost.js` into a new `productService.resolveVariantMedia()` method". Do not include code snippets or exact implementations — the agent will figure that out. Keep steps scoped to the finding; do not combine multiple findings into one plan.

Skip scope areas with no findings. If the change is clean, say so in one sentence — do not pad with praise or generic observations.
