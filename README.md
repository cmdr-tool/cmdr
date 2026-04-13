<p align="center">
  <img src="web/static/cmdr-logo.svg" alt="cmd+r" width="200" />
</p>

<h1 align="center">cmdr</h1>

<p align="center">
  Personal commander portal for managing workstreams, sessions, and automation.
</p>

---

A Go daemon + SvelteKit dashboard + native macOS app that runs as a background service. Surfaces tmux sessions, Claude Code instances, git activity, and scheduled tasks in a single warm, bourbon-themed UI.

## What it does

- **Session dashboard** — live view of all tmux sessions with working directory, running processes, and one-click switching
- **Claude Code tracking** — detects active Claude instances, shows working/waiting/idle status by reading tmux pane state
- **Git commit tracking** — monitors local repos, surfaces new commits with diffs (via difft), and marks as seen
- **Directives & delegation** — draft structured directives for Claude, dispatch review/design/implementation tasks
- **Task scheduler** — cron-based task runner with a web UI for monitoring and manual execution
- **Real-time updates** — SSE event stream pushes state changes to the browser
- **Native macOS app** — frameless WKWebView wrapper with menu bar icon, lives in `/Applications`
- **macOS daemon** — runs via launchd at login, always on

## Quick start

```bash
# Install frontend deps
bun install --cwd web

# Dev mode (Vite HMR, proxies API to production daemon on :7369)
make dev

# Build everything (frontend + backend + native app)
make build

# Production install (build + install binary + app + restart launchd service)
make install
```

## Stack

| | |
|---|---|
| **Backend** | Go, Cobra CLI, robfig/cron, SQLite, Unix sockets |
| **Frontend** | SvelteKit (SPA), Tailwind CSS v4, Lucide icons |
| **Native app** | Swift, WKWebView, AppKit |
| **Fonts** | Orbitron (headings), Space Grotesk (body) |
| **Tooling** | bun, make |
| **Platform** | macOS (launchd) |

## License

Private — personal use.
