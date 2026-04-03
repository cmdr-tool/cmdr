<p align="center">
  <img src="web/static/cmdr-logo.svg" alt="cmd+r" width="200" />
</p>

<h1 align="center">cmdr</h1>

<p align="center">
  Personal commander portal for managing workstreams, sessions, and automation.
</p>

---

A Go daemon + SvelteKit dashboard that runs as a macOS background service. Surfaces tmux sessions, Claude Code instances, and scheduled tasks in a single warm, bourbon-themed UI.

## What it does

- **Session dashboard** — live view of all tmux sessions with working directory, running processes, and one-click switching
- **Claude Code tracking** — detects active Claude instances, shows working/waiting/idle status by reading tmux pane state
- **Task scheduler** — cron-based task runner with a web UI for monitoring and manual execution
- **Real-time updates** — SSE event stream pushes tmux and Claude state changes to the browser every 5s
- **macOS daemon** — runs via launchd at login, always on

## Quick start

```bash
# Install deps
bun install --cwd web

# Dev mode (Go daemon with hot-reload + Vite dev server)
bun run dev

# Production install (builds binary + installs launchd service)
./scripts/install.sh
```

## Stack

| | |
|---|---|
| **Backend** | Go, Cobra CLI, robfig/cron, Unix sockets |
| **Frontend** | SvelteKit (SPA), Tailwind CSS v4, Lucide icons |
| **Fonts** | Orbitron (headings), Space Grotesk (body) |
| **Tooling** | bun, air (hot-reload), concurrently |
| **Platform** | macOS (launchd) |

## License

Private — personal use.
