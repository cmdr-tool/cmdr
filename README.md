<p align="center">
  <img src="web/static/cmdr-logo.svg" alt="cmd+r" width="200" />
</p>

<h1 align="center">cmdr</h1>

<p align="center">
  Commander portal for managing workstreams, sessions, and automation.
</p>

---

A Go daemon + SvelteKit dashboard + native macOS app that runs as a background service. Surfaces terminal sessions, Claude Code instances, git activity, and scheduled tasks in a single warm, bourbon-themed UI.

## What it does

- **Session dashboard** — live view of terminal sessions with working directory, running processes, and one-click switching
- **Claude Code tracking** — detects active Claude instances, shows working/waiting/idle status
- **Git commit tracking** — monitors local repos, surfaces new commits with diffs (via difft), and marks as seen
- **Directives & delegation** — draft structured directives for Claude, dispatch review/design/implementation tasks across repos via squads
- **Task scheduler** — cron-based task runner with a web UI for monitoring and manual execution
- **Real-time updates** — SSE event stream pushes state changes to the browser
- **Native macOS app** — frameless WKWebView wrapper with menu bar icon, lives in `/Applications`
- **macOS daemon** — runs via launchd at login, always on

## Prerequisites

cmdr orchestrates your terminal environment — without these tools installed, it has nothing to manage.

| | What | Supported | Default |
|---|---|---|---|
| **Terminal multiplexer** | Manages sessions, windows, and panes that cmdr monitors and controls | [tmux](https://github.com/tmux/tmux), [cmux](https://github.com/manaflow-ai/cmux) | tmux |
| **Terminal emulator** | The app cmdr brings to foreground when switching sessions or opening files | [Ghostty](https://ghostty.org), [WezTerm](https://wezfurlong.org/wezterm/), [cmux](https://github.com/manaflow-ai/cmux), any macOS app | Ghostty |
| **Code editor** | Launched in terminal panes for file navigation; must be invokable from the command line | nvim, vim, [zed](https://zed.dev), [code](https://code.visualstudio.com) (VS Code), etc. | nvim |

All three are configured during `make install` and can be changed later with `make configure`.

## Build requirements

- **macOS** (launchd)
- **Go** 1.22+
- **bun** (frontend tooling)

## Quick start

```bash
# Install frontend deps
bun install --cwd web

# Build + install (prompts for config on first run)
make install
```

On first run, `make install` launches an interactive setup that writes `~/.cmdr/cmdr.env`:

| Setting | Default | Description |
|---|---|---|
| `CMDR_LABEL` | `com.cmdr-tool.cmdr` | launchd agent label |
| `CMDR_CODE_DIR` | `~/Code` | root directory for git repo monitoring |
| `CMDR_OLLAMA_URL` | `http://localhost:11434` | Ollama server for title summarization |
| `CMDR_OLLAMA_MODEL` | `gemma4:e4b` | Ollama model |

To change settings later: `make configure` then `make install`.

## Terminal adapters

cmdr uses a pluggable terminal adapter system. Set `CMDR_MULTIPLEXER` and `CMDR_TERMINAL_APP` environment variables (in the launchd plist or `~/.cmdr/cmdr.env`):

| Adapter | `CMDR_MULTIPLEXER` | `CMDR_TERMINAL_APP` | Notes |
|---|---|---|---|
| **tmux** (default) | `tmux` | `Ghostty` | Full feature support |
| **cmux** | `cmux` | `cmux` | PID/process detection unavailable; Claude enrichment degrades gracefully |

Adding a new adapter: implement the `terminal.Multiplexer` interface (9 methods) in `internal/terminal/adapters/<name>/` and register via `init()`. See `internal/terminal/terminal.go` for the interface definition.

## Development

```bash
# Vite HMR dev server, proxies API to production daemon on :7369
make dev

# Build without installing
make build

# Run Go tests
make test
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

MIT
