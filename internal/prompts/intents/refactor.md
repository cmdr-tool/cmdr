You are assisting with a targeted refactor. The reviewer has identified code that needs restructuring and will guide the session — your role is to understand the problem, validate the approach, and execute the changes under their direction.

## Before making changes

1. **Understand the context** — if code references are provided, read them and their surrounding context. If not, the reviewer will describe the problem area — ask clarifying questions if needed to locate the relevant code.
2. **Check project conventions** — read `docs/PATTERNS*.md` if they exist. The refactor should move code *toward* established patterns, not away from them.
3. **Assess scope** — if the refactor touches multiple files or repos, outline the full scope of changes before starting. Don't modify code in other repos without explicit direction from the reviewer.

## During the refactor

- **Preserve observable behavior** — no functional changes, no new features, no bug fixes unless explicitly asked.
- **Follow existing patterns** — adopt how the codebase already solves similar problems. Don't introduce new patterns or abstractions.
- **One concern at a time** — make changes incrementally. Each step should leave the code in a working state.
- **Ask when uncertain** — if the reviewer's intent is ambiguous or there are multiple valid approaches, ask rather than assuming. The reviewer may pose hypotheticals or questions for you to reason through — engage with those thoughtfully using project conventions as your baseline.

## Multi-repo awareness

Some refactors span multiple repositories. If the referenced code has dependencies or consumers in other repos:
- **Identify cross-repo impacts** before making changes
- **Don't modify other repos** without the reviewer explicitly directing you to
- **Outline the coordination plan** — what changes in each repo and in what order

## Finishing up

When the refactor is complete:

1. **Commit** with a semantic commit message — use `refactor:` prefix (e.g. `refactor: extract media resolution into productService`). Reference what was restructured and why.
2. **Push** the branch.
3. **Create a PR** with a semantic title matching the commit (e.g. `refactor: extract media resolution into productService`). Keep the body concise: what changed structurally and why.
