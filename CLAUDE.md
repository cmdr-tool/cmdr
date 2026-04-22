# CLAUDE.md

This file provides guidance to coding agents working with this repository.

## Project Overview

Cmdr is a personal "commander portal" — a Go backend daemon paired with a SvelteKit frontend SPA. The Go daemon handles task scheduling and execution; the SvelteKit app provides a web UI with router-based navigation.

## Key Entrypoints

- **`cmd/cmdr/main.go`** — Main CLI entrypoint. Defines Cobra commands, wires the embedded SPA into the daemon, and is the best starting point for backend flow.
- **`internal/daemon/daemon.go`** — Daemon bootstrap, adapter resolution, DB/scheduler startup, and `registerAPI()` route registration.
- **`internal/daemon/handle_*.go`** — HTTP handlers grouped by domain (tasks, sessions, git, review, squads, editor, etc.).
- **`internal/scheduler/scheduler.go`** — Built-in scheduled task registration in `New()` / `register()`.
- **`internal/tasks/`** — Task implementations used by the scheduler.
- **`web/src/lib/api.ts`** — Frontend API client; update this when backend API shapes or endpoints change.
- **`web/src/routes/+layout.svelte`** — App shell, navigation, startup store initialization, and top-level status display.
- **`web/src/routes/+layout.ts`** — Disables SSR for the SPA.
- **`web/src/routes/+page.svelte`** — Dashboard route.
- **`web/src/routes/settings/+page.svelte`** — Settings UI.
- **`web/svelte.config.js`** — Static adapter config with SPA fallback (`index.html`).

## Build & Run Commands

```bash
make build     # build frontend (SPA) + backend (Go binary with embedded SPA)
make install   # build + install binary + restart launchd service
make dev       # Vite HMR dev server, proxies API to production daemon (:7369)
make test      # run Go tests
make clean     # remove build artifacts
```

### CLI

```bash
go run ./cmd/cmdr list        # list registered tasks
go run ./cmd/cmdr run <task>  # execute a task immediately
go run ./cmd/cmdr status      # check daemon status
```

### Dev Workflow

The production daemon (via launchd) serves both the API and the embedded SPA on `:7369`. For frontend development, `make dev` starts only Vite with HMR on `:5370`, proxying `/api` calls to the production daemon. No separate dev backend needed.

### macOS Service (launchd)

The daemon runs as a launchd user agent whose label is chosen at setup time (default `com.cmdr-tool.cmdr`) and stored in `~/.cmdr/cmdr.env`. `make install` runs `scripts/setup.sh` on first run to populate that env file, then renders `com.cmdr.plist.tpl` into `~/Library/LaunchAgents/<label>.plist`. Logs go to `/tmp/cmdr.out.log` and `/tmp/cmdr.err.log`.

## Validation / Definition of Done

Run the smallest relevant validation set for the change you made:

- **Backend-only changes**: `go test ./...`
- **Frontend-only changes**: `cd web && bun run check`
- **Frontend build/config changes**: `cd web && bun run build`
- **Cross-stack or release-sensitive changes**: `make build`
- **Service/install flow changes**: prefer validating with `make install` only when needed, because it rebuilds, reinstalls, and restarts the launchd agent

If you change API contracts, scheduled task behavior, embedded frontend assets, or app boot flow, prefer `make build` before considering the work done.

## Environment & Tooling Assumptions

- This is a **macOS-first** project.
- Use **`bun` for all frontend tasks**; do not use `npm`.
- The backend embeds the built SPA from `web/build`, so frontend builds can affect backend validation.
- `tmux` is the default multiplexer; `cmux` is supported via `CMDR_MULTIPLEXER`.
- The Apple summarizer requires macOS 15.1+ and Apple Silicon; Ollama is the fallback pluggable summarizer path.
- Some git/diff flows use `difft` when available and fall back to `git show`.

## Gotchas / Don’t Forget

- The frontend is a static SPA embedded into the Go binary from `web/build`; after UI or frontend build changes, rebuild before validating production behavior.
- `make dev` does **not** start a separate backend. It uses Vite HMR and proxies `/api` requests to the already-running production daemon on `:7369`.
- `make install` is heavier than a normal validation step: it rebuilds artifacts, reinstalls binaries/app assets, rewrites the launchd plist, and restarts the user agent.
- Launchd config, runtime env, logs, and database state live outside the repo. If behavior looks inconsistent, inspect `~/.cmdr/cmdr.env`, `~/Library/LaunchAgents/`, `/tmp/cmdr.out.log`, `/tmp/cmdr.err.log`, and `~/.cmdr/cmdr.db`.
- API routes are intentionally registered for different consumers; do not assume every route exists only under `/api`.
- If you change backend response shapes or endpoint names, update `web/src/lib/api.ts` and any dependent stores/components in the same change.
- If a frontend change seems correct but the app still behaves strangely in production, suspect stale embedded assets or daemon/service state before assuming the UI code is wrong.
- Multiplexer/editor behavior may differ between `tmux` and `cmux`; avoid assuming process-detection or pane-reuse capabilities exist in both adapters.

## Conventions

- **Commits**: Use semantic/conventional commit messages: `feat:`, `fix:`, `refactor:`, `chore:`, `docs:`, `style:`, `test:`. Keep the subject concise.
- **Tailwind only**: No custom CSS classes in components. Use utility classes.
- **Orbitron**: Only use at even pixel sizes (10, 12, 14) — odd sizes cause artifacts.

## Architecture

### Backend

- **`cmd/cmdr/`** — CLI entry point using Cobra. Subcommands: `start`, `stop`, `status`, `run`, `list`.
- **`internal/daemon/`** — Daemon lifecycle with dual listeners: Unix socket for CLI IPC and TCP for the web UI. Environment-aware paths/ports via `CMDR_ENV`. API routes are registered with and without `/api` prefix.
- **`internal/scheduler/`** — Wraps `robfig/cron/v3` with seconds precision. Tasks are registered in `New()` with cron expressions.
- **`internal/agent/`** — Pluggable agent adapter system. `agent.go` defines the `Agent` interface (RunSimple, RunStreaming, InteractiveCommand, ResumeCommand), `Capabilities` struct, and adapter registry. Same Register/New pattern as terminal and summarizer. Claude is the default adapter (`internal/agent/claude/`).
- **`internal/tasks/`** — Individual task implementations. Tasks that need dependencies (e.g. `*sql.DB`) return closures. Add new tasks here and register them in the scheduler.
- **`internal/terminal/`** — Pluggable terminal adapter system. `terminal.go` defines the `Multiplexer` interface (10 methods) and `Emulator` interface, plus shared helpers (`SessionName`, `FindWindowTarget`, editor utilities). Adapters live in `adapters/<name>/` and register via `init()`. Selected at startup by `CMDR_MULTIPLEXER` env var (default `tmux`).
- **`internal/terminal/adapters/tmux/`** — Tmux adapter. Session listing via `list-panes -a`, worktree-aware naming, editor pane reuse by process detection.
- **`internal/terminal/adapters/cmux/`** — [cmux](https://github.com/manaflow-ai/cmux) adapter. Workspace/surface management via cmux CLI subprocess. In-memory ref map rebuilt on each ListSessions. Known limitations: no PID/process detection, editor always creates fresh surfaces.
- **`internal/summarizer/`** — Pluggable title summarizer. `Summarizer` interface with adapter registry, same pattern as terminal. Selected by `CMDR_SUMMARIZER` env var (default `apple`).
- **`internal/summarizer/apple/`** — Apple Intelligence adapter. Spawns `cmdr-summarize` Swift binary (in `tools/cmdr-summarize/`), reads JSON result. Requires macOS 15.1+, Apple Silicon.
- **`internal/summarizer/ollama/`** — Ollama adapter. Wraps `internal/ollama/` for HTTP-based summarization.
- **`internal/ollama/`** — Thin Ollama API client. Uses tool calling for structured output. Configured via `CMDR_OLLAMA_URL` (default `http://localhost:11434`) and `CMDR_OLLAMA_MODEL` (default `gemma4:e4b`).
- **`internal/db/`** — SQLite database (`~/.cmdr/cmdr.db`) using `modernc.org/sqlite` (pure Go). Schema migrations run on startup. Tables: `repos` (local git repos by path), `commits` (tracked commits with seen state), `agent_tasks` (task lifecycle with `terminal_target` for adapter-native refs, `agent` column tracking which agent handled the task), `agentic_tasks` (user-configurable scheduled tasks).
- **`internal/gitlocal/`** — Local git repo integration. Discovers repos under `CMDR_CODE_DIR` (default `~/Code`), fetches via `git fetch`, reads commits via `git log`, diffs via `difft` (falls back to `git show`). All operations use local filesystem, no GitHub API.
- **`tools/cmdr-summarize/`** — Swift binary using `FoundationModels` for on-device title generation. Built by `make install`, installed alongside `cmdr` in `~/.local/bin/`.

### Frontend

- **SvelteKit SPA** (`web/`) using `adapter-static` with `fallback: 'index.html'` for client-side routing. SSR is disabled (`ssr = false` in root layout).
- **Tailwind CSS v4** for styling — use utility classes only, no custom CSS classes.
- **File-based routes under `web/src/routes/`** drive the SPA screens; shared API calls live in `web/src/lib/api.ts`.

### Design System

"Dark Bourbon" theme — warm, cozy dark UI. Full reference with palette, typography, component snippets, and layout patterns in [`docs/DESIGN.md`](docs/DESIGN.md). Color tokens defined in `web/src/app.css` via Tailwind v4 `@theme`.

Key rules:
- **Orbitron** (`font-display`) for headings/labels/buttons, **Space Grotesk** (`font-sans`) for body text
- Tailwind utility classes only — no custom CSS classes
- `bourbon-*` for surfaces/text, `cmd-*` (purple) for interactive elements, `run-*` (amber) for status/labels

### Adding a New Task

1. Create a function in `internal/tasks/` that returns `error`
2. Register it in `internal/scheduler/New()` with a name, description, cron schedule, and the function

### Common Change Workflows

- **When adding or changing an API endpoint**:
  1. Update route registration in `internal/daemon/daemon.go` (`registerAPI()`)
  2. Add or modify the handler in the appropriate `internal/daemon/handle_*.go` file
  3. Update `web/src/lib/api.ts` if the frontend consumes that endpoint
  4. Update the relevant Svelte route/store/component that uses the data

- **When changing scheduled task behavior**:
  1. Update the task implementation in `internal/tasks/`
  2. Update registration or schedule metadata in `internal/scheduler/scheduler.go`
  3. Validate with `go test ./...` and, if behavior affects runtime wiring, `make build`

- **When changing agent, summarizer, or terminal adapter behavior**:
  1. Check the shared interface in `internal/agent/`, `internal/summarizer/`, or `internal/terminal/`
  2. Update the relevant adapter(s)
  3. If changing shared terminal logic, consider both `tmux` and `cmux` behavior

- **When changing frontend navigation or top-level app behavior**:
  1. Update route components under `web/src/routes/`
  2. Update shared navigation/app shell in `web/src/routes/+layout.svelte` if needed
  3. Keep `web/src/lib/api.ts` types aligned with backend responses

- **When making UI changes**:
  1. Read `docs/DESIGN.md` first for the Dark Bourbon design system
  2. Use Tailwind utilities only
  3. Preserve Orbitron sizing constraints and existing token usage in `web/src/app.css`

### Agent Overrides

Override files in `~/.cmdr/agents/<task-type>.md` route specific headless task types to alternative agents with custom prompts. Loaded once at daemon startup.

Supported task types: `review`, `analysis`

```markdown
---
agent: pi              # registered adapter name
output: html           # "markdown"/"md" (default), "html", or "text"/"plain"
---

Custom system prompt for this task type...
```

The body replaces the default system prompt. The existing prompt template (e.g. `review.md` with diff data) is still used as the main prompt. If no override file exists for a task type, the default agent (Claude) is used with the built-in prompt.

### API Surface

- **Backend route registration:** `internal/daemon/daemon.go` in `registerAPI()`
- **Handler implementations:** `internal/daemon/handle_*.go`
- **Frontend API wrappers and response types:** `web/src/lib/api.ts`
- When changing routes or API shapes, update backend registration, handler logic, frontend client/types, and calling UI in the same change.
