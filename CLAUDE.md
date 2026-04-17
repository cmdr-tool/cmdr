# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Project Overview

Cmdr is a personal "commander portal" — a Go backend daemon paired with a SvelteKit frontend SPA. The Go daemon handles task scheduling and execution; the SvelteKit app provides a web UI with router-based navigation. Use `bun` (not npm) for all frontend operations.

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

## Conventions

- **Commits**: Use semantic/conventional commit messages: `feat:`, `fix:`, `refactor:`, `chore:`, `docs:`, `style:`, `test:`. Keep the subject concise.
- **Tailwind only**: No custom CSS classes in components. Use utility classes.
- **Orbitron**: Only use at even pixel sizes (10, 12, 14) — odd sizes cause artifacts.

## Architecture

### Backend

- **`cmd/cmdr/`** — CLI entry point using Cobra. Subcommands: `start`, `stop`, `status`, `run`, `list`.
- **`internal/daemon/`** — Daemon lifecycle with dual listeners: Unix socket for CLI IPC and TCP for the web UI. Environment-aware paths/ports via `CMDR_ENV`. API routes are registered with and without `/api` prefix.
- **`internal/scheduler/`** — Wraps `robfig/cron/v3` with seconds precision. Tasks are registered in `New()` with cron expressions.
- **`internal/tasks/`** — Individual task implementations. `Claude()` helper shells out to `claude -p` CLI. Tasks that need dependencies (e.g. `*sql.DB`) return closures. Add new tasks here and register them in the scheduler.
- **`internal/terminal/`** — Pluggable terminal adapter system. `terminal.go` defines the `Multiplexer` interface (10 methods) and `Emulator` interface, plus shared helpers (`SessionName`, `FindWindowTarget`, editor utilities). Adapters live in `adapters/<name>/` and register via `init()`. Selected at startup by `CMDR_MULTIPLEXER` env var (default `tmux`).
- **`internal/terminal/adapters/tmux/`** — Tmux adapter. Session listing via `list-panes -a`, worktree-aware naming, editor pane reuse by process detection.
- **`internal/terminal/adapters/cmux/`** — [cmux](https://github.com/manaflow-ai/cmux) adapter. Workspace/surface management via cmux CLI subprocess. In-memory ref map rebuilt on each ListSessions. Known limitations: no PID/process detection, editor always creates fresh surfaces.
- **`internal/summarizer/`** — Pluggable title summarizer. `Summarizer` interface with adapter registry, same pattern as terminal. Selected by `CMDR_SUMMARIZER` env var (default `apple`).
- **`internal/summarizer/apple/`** — Apple Intelligence adapter. Spawns `cmdr-summarize` Swift binary (in `tools/cmdr-summarize/`), reads JSON result. Requires macOS 15.1+, Apple Silicon.
- **`internal/summarizer/ollama/`** — Ollama adapter. Wraps `internal/ollama/` for HTTP-based summarization.
- **`internal/ollama/`** — Thin Ollama API client. Uses tool calling for structured output. Configured via `CMDR_OLLAMA_URL` (default `http://localhost:11434`) and `CMDR_OLLAMA_MODEL` (default `gemma4:e4b`).
- **`internal/db/`** — SQLite database (`~/.cmdr/cmdr.db`) using `modernc.org/sqlite` (pure Go). Schema migrations run on startup. Tables: `repos` (local git repos by path), `commits` (tracked commits with seen state), `task_config` (schedule/enabled overrides), `claude_tasks` (task lifecycle with `terminal_target` for adapter-native refs).
- **`internal/gitlocal/`** — Local git repo integration. Discovers repos under `CMDR_CODE_DIR` (default `~/Code`), fetches via `git fetch`, reads commits via `git log`, diffs via `difft` (falls back to `git show`). All operations use local filesystem, no GitHub API.
- **`tools/cmdr-summarize/`** — Swift binary using `FoundationModels` for on-device title generation. Built by `make install`, installed alongside `cmdr` in `~/.local/bin/`.

### Frontend

- **SvelteKit SPA** (`web/`) using `adapter-static` with `fallback: 'index.html'` for client-side routing. SSR is disabled (`ssr = false` in root layout).
- **Tailwind CSS v4** for styling — use utility classes only, no custom CSS classes.
- **`web/src/lib/api.ts`** — Typed API client for daemon communication (`/api/status`, `/api/tasks`, `/api/run`).
- **`web/src/routes/`** — File-based routing. Dashboard (`/`) and Settings (`/settings`).

### Design System

"Dark Bourbon" theme — warm, cozy dark UI. Full reference with palette, typography, component snippets, and layout patterns in [`docs/DESIGN.md`](docs/DESIGN.md). Color tokens defined in `web/src/app.css` via Tailwind v4 `@theme`.

Key rules:
- **Orbitron** (`font-display`) for headings/labels/buttons, **Space Grotesk** (`font-sans`) for body text
- Tailwind utility classes only — no custom CSS classes
- `bourbon-*` for surfaces/text, `cmd-*` (purple) for interactive elements, `run-*` (amber) for status/labels

### Adding a New Task

1. Create a function in `internal/tasks/` that returns `error`
2. Register it in `internal/scheduler/New()` with a name, description, cron schedule, and the function

### API Endpoints

| Endpoint | Method | Description |
|---|---|---|
| `/api/status` | GET | Daemon status (pid, task count) |
| `/api/tasks` | GET | List all registered tasks |
| `/api/run?task=` | GET/POST | Execute a task by name |
| `/api/tmux/sessions` | GET | List all tmux sessions with windows/panes |
| `/api/tmux/sessions/create` | POST | Create a new tmux session `{"dir": "/path"}` |
| `/api/repos` | GET | List monitored local repos |
| `/api/repos/discover` | GET | Scan `CMDR_CODE_DIR` for git repos not yet monitored |
| `/api/repos/add` | POST | Add a local repo to monitor `{"path": "/path/to/repo", ...}` |
| `/api/repos/remove` | POST | Remove a monitored repo `{"id": 1}` |
| `/api/commits` | GET | List commits (query: `repo`, `unseen`, `limit`) |
| `/api/commits/files` | GET | List files changed in a commit (query: `repo` path, `sha`) |
| `/api/commits/diff` | GET | Get diff for a commit via difft/git (query: `repo` path, `sha`) |
| `/api/commits/seen` | POST | Mark commits as seen `{"ids": [1,2,3]}` |
| `/api/repos/sync` | POST | Trigger `git fetch` + commit sync for all monitored repos |
| `/api/repos/pull` | POST | Fast-forward/rebase local branch to origin `{"repoPath": "..."}` |
| `/api/editor/open` | POST | Open file in nvim via tmux `{"repoPath", "file", "line"}` |
| `/api/squads/enlist` | POST | Enlist a squad member `{"squad", "from", "to", "summary", "details"}` |
