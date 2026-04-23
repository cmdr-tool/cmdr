// Package cmux implements the terminal.Multiplexer interface using the cmux
// native macOS terminal app (https://github.com/manaflow-ai/cmux).
// Communication is via the cmux CLI binary, which handles socket auth internally.
//
// Known limitations vs the tmux adapter:
//   - Pane.PID is always zero — Claude session enrichment via PID ancestry
//     is not available; name-based fallback matching is used instead.
//   - Pane.Command and Pane.CWD are parsed from surface titles heuristically
//     and may be empty when titles don't follow expected formats.
//   - analytics.determineActiveTool returns "inactive" under cmux.
//   - No remain-on-exit equivalent — if Claude exits with an error the
//     surface closes immediately (degraded error visibility).
package cmux

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/cmdr-tool/cmdr/internal/terminal"
)

func init() {
	terminal.Register("cmux", func() terminal.Multiplexer {
		return &Adapter{}
	})
}

// Adapter implements terminal.Multiplexer via the cmux CLI.
type Adapter struct {
	mu         sync.RWMutex
	workspaces map[string]string // workspace title → workspace ref
	activeSurf map[string]string // workspace title → focused surface ref
}

// --- CLI client ---

// bin returns the path to the cmux CLI binary.
func bin() string {
	if p := os.Getenv("CMUX_BIN"); p != "" {
		return p
	}
	known := "/Applications/cmux.app/Contents/Resources/bin/cmux"
	if _, err := os.Stat(known); err == nil {
		return known
	}
	return "cmux"
}

// run executes the cmux CLI and returns trimmed stdout.
func run(args ...string) (string, error) {
	cmd := exec.Command(bin(), args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("cmux %s: %w (%s)", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return strings.TrimSpace(string(out)), nil
}

// parseRef extracts the first ref from an "OK ref1 ref2 ..." CLI response.
func parseRef(out string) (string, error) {
	parts := strings.Fields(out)
	if len(parts) < 2 || parts[0] != "OK" {
		return "", fmt.Errorf("cmux: unexpected response: %q", out)
	}
	return parts[1], nil
}

// --- CLI JSON types ---

type treeResult struct {
	Windows []windowInfo `json:"windows"`
}

type windowInfo struct {
	Ref        string          `json:"ref"`
	Workspaces []workspaceInfo `json:"workspaces"`
}

type workspaceInfo struct {
	Ref    string     `json:"ref"`
	Title  string     `json:"title"`
	Active bool       `json:"active"`
	Panes  []paneInfo `json:"panes"`
}

type paneInfo struct {
	Ref      string        `json:"ref"`
	Focused  bool          `json:"focused"`
	Surfaces []surfaceInfo `json:"surfaces"`
}

type surfaceInfo struct {
	Ref     string `json:"ref"`
	Title   string `json:"title"`
	Focused bool   `json:"focused"`
}

// --- Multiplexer interface ---

func (a *Adapter) ListSessions() ([]terminal.Session, error) {
	tree, err := a.listTree()
	if err != nil {
		return []terminal.Session{}, nil
	}

	// Rebuild workspace and active-surface maps while iterating
	wsMap := make(map[string]string)
	surfMap := make(map[string]string)

	var sessions []terminal.Session
	for _, win := range tree.Windows {
		for _, ws := range win.Workspaces {
			wsMap[ws.Title] = ws.Ref

			s := terminal.Session{
				Name:     ws.Title,
				Attached: ws.Active,
			}
			for pi, pane := range ws.Panes {
				w := terminal.Window{
					Index:  pi,
					Name:   pane.Ref,
					Active: pane.Focused,
				}
				for si, surf := range pane.Surfaces {
					cmd, cwd := parseSurfaceTitle(surf.Title)
					if surf.Focused {
						surfMap[ws.Title] = surf.Ref
					}
					w.Panes = append(w.Panes, terminal.Pane{
						Index:   si,
						PID:     0, // not available in cmux
						Active:  surf.Focused,
						CWD:     cwd,
						Command: cmd,
					})
				}
				s.Windows = append(s.Windows, w)
			}
			sessions = append(sessions, s)
		}
	}

	a.mu.Lock()
	a.workspaces = wsMap
	a.activeSurf = surfMap
	a.mu.Unlock()

	return sessions, nil
}

// parseSurfaceTitle extracts a command name and CWD from a terminal surface title.
// Handles common shell title formats like "zsh — ~/code/project" or "nvim - file.go".
// cmux abbreviates the home directory as "…" (U+2026) in surface titles, e.g. "…/code/project".
// The "../" prefix is also handled for robustness (some shells use it similarly).
func parseSurfaceTitle(title string) (command, cwd string) {
	title = strings.TrimSpace(title)
	if title == "" {
		return "", ""
	}
	for _, sep := range []string{" — ", " – ", " - "} {
		if idx := strings.LastIndex(title, sep); idx >= 0 {
			left := strings.TrimSpace(title[:idx])
			right := strings.TrimSpace(title[idx+len(sep):])
			if isPathLike(right) {
				return left, expandHome(right)
			}
		}
	}
	// Title is a bare path (no command prefix).
	if isPathLike(title) {
		return "", expandHome(title)
	}
	return title, ""
}

// isPathLike reports whether s looks like a filesystem path.
// Recognises ~/, /, cmux's ../ abbreviation, and cmux's …/ (U+2026) abbreviation.
func isPathLike(s string) bool {
	return strings.HasPrefix(s, "~") ||
		strings.HasPrefix(s, "/") ||
		strings.HasPrefix(s, "../") ||
		strings.HasPrefix(s, "…/") // U+2026 used by cmux as home-dir prefix
}

// expandHome expands ~, ../, and cmux's …/ home-dir abbreviations to absolute paths.
func expandHome(path string) string {
	home, err := os.UserHomeDir()
	if err != nil {
		return path
	}
	switch {
	case path == "~" || path == ".." || path == "…":
		return home
	case strings.HasPrefix(path, "~/"):
		return filepath.Join(home, path[2:])
	case strings.HasPrefix(path, "../"):
		return filepath.Join(home, path[3:])
	case strings.HasPrefix(path, "…/"):
		return filepath.Join(home, strings.TrimPrefix(path, "…/"))
	default:
		return path
	}
}

func (a *Adapter) CreateSession(dir string) (string, error) {
	name := terminal.SessionName(dir)

	// Check if workspace already exists
	if ref := a.resolveWorkspace(name); ref != "" {
		return name, nil
	}

	out, err := run("new-workspace", "--cwd", dir)
	if err != nil {
		return "", fmt.Errorf("cmux: create workspace: %w", err)
	}
	wsRef, err := parseRef(out)
	if err != nil {
		return "", err
	}

	// Rename to the session name (new-workspace has no --title flag)
	if _, err := run("rename-workspace", "--workspace", wsRef, name); err != nil {
		log.Printf("cmux: rename workspace %s → %s: %v", wsRef, name, err)
	}

	a.mu.Lock()
	if a.workspaces == nil {
		a.workspaces = make(map[string]string)
	}
	a.workspaces[name] = wsRef
	a.mu.Unlock()

	return name, nil
}

func (a *Adapter) KillSession(name string) error {
	wsRef := a.resolveWorkspace(name)
	if wsRef == "" {
		return nil // already closed
	}
	if _, err := run("close-workspace", "--workspace", wsRef); err != nil {
		return err
	}
	a.mu.Lock()
	delete(a.workspaces, name)
	delete(a.activeSurf, name)
	a.mu.Unlock()
	return nil
}

func (a *Adapter) SwitchClient(name string) error {
	wsRef := a.resolveWorkspace(name)
	if wsRef == "" {
		return fmt.Errorf("cmux: workspace not found: %s", name)
	}
	_, err := run("select-workspace", "--workspace", wsRef)
	return err
}

func (a *Adapter) CreateWindow(session, windowName, dir, shellCmd string) (string, error) {
	wsRef := a.resolveWorkspace(session)
	if wsRef == "" {
		// Workspace not found — create it
		if _, err := a.CreateSession(dir); err != nil {
			return "", err
		}
		wsRef = a.resolveWorkspace(session)
		if wsRef == "" {
			return "", fmt.Errorf("cmux: workspace %q not found after creation", session)
		}
	}

	out, err := run("new-surface", "--workspace", wsRef)
	if err != nil {
		return "", fmt.Errorf("cmux: create surface: %w", err)
	}
	surfRef, err := parseRef(out)
	if err != nil {
		return "", err
	}

	// Send the command to the new surface
	if _, err := run("send", "--surface", surfRef, shellCmd+"\n"); err != nil {
		return "", fmt.Errorf("cmux: send command: %w", err)
	}

	// Switch to workspace so the user sees it
	run("select-workspace", "--workspace", wsRef)

	// Return the surface ref as the opaque target
	return surfRef, nil
}

func (a *Adapter) KillWindow(target string) error {
	if _, err := run("close-surface", "--surface", target); err != nil {
		log.Printf("cmux: close-surface %s: %v", target, err)
	}
	return nil
}

func (a *Adapter) SendKeys(target, keys string, literal bool) error {
	if literal {
		_, err := run("send", "--surface", target, keys+"\n")
		return err
	}
	_, err := run("send-key", "--surface", target, keys)
	return err
}

func (a *Adapter) CapturePane(target string, lines int) (string, error) {
	out, err := run("read-screen", "--surface", target, "--lines", strconv.Itoa(lines))
	if err == nil {
		return out, nil
	}
	// target may be a workspace name (from name-based session matching) — try active surface
	a.mu.RLock()
	surfRef := a.activeSurf[target]
	a.mu.RUnlock()
	if surfRef == "" {
		return "", nil
	}
	out, err = run("read-screen", "--surface", surfRef, "--lines", strconv.Itoa(lines))
	if err != nil {
		return "", nil
	}
	return out, nil
}

func (a *Adapter) OpenInEditor(dir, file string, line int) (*terminal.EditorTarget, error) {
	sessionName := terminal.SessionName(dir)

	// cmux can't detect running processes — always create a fresh surface
	sessName, err := a.CreateSession(dir)
	if err != nil {
		return nil, fmt.Errorf("creating session for %s: %w", dir, err)
	}
	target, err := a.CreateWindow(sessName, "editor", dir, terminal.EditorOpenCmd(file, line))
	if err != nil {
		return nil, fmt.Errorf("creating editor surface: %w", err)
	}
	return &terminal.EditorTarget{Session: sessionName, Target: target, Fresh: true}, nil
}

func (a *Adapter) WindowExists(target string) bool {
	tree, err := a.listTree()
	if err != nil {
		return false
	}
	for _, win := range tree.Windows {
		for _, ws := range win.Workspaces {
			for _, pane := range ws.Panes {
				for _, surf := range pane.Surfaces {
					if surf.Ref == target {
						return true
					}
				}
			}
		}
	}
	return false
}

// --- Internal helpers ---

func (a *Adapter) listTree() (treeResult, error) {
	out, err := run("tree", "--all", "--json")
	if err != nil {
		return treeResult{}, err
	}
	var tree treeResult
	return tree, json.Unmarshal([]byte(out), &tree)
}

// resolveWorkspace finds a workspace ref by session name.
// Checks the in-memory map first, falls back to a tree scan.
func (a *Adapter) resolveWorkspace(name string) string {
	a.mu.RLock()
	ref := a.workspaces[name]
	a.mu.RUnlock()
	if ref != "" {
		return ref
	}

	// Fallback: scan tree
	tree, err := a.listTree()
	if err != nil {
		return ""
	}
	for _, win := range tree.Windows {
		for _, ws := range win.Workspaces {
			if ws.Title == name {
				a.mu.Lock()
				if a.workspaces == nil {
					a.workspaces = make(map[string]string)
				}
				a.workspaces[name] = ws.Ref
				a.mu.Unlock()
				return ws.Ref
			}
		}
	}
	return ""
}

func (a *Adapter) CandidatePanes(sessions []terminal.Session) []terminal.CandidatePane {
	var panes []terminal.CandidatePane
	terminal.ForEachPane(sessions, func(target string, p *terminal.Pane) {
		panes = append(panes, terminal.CandidatePane{
			Target:  target,
			Session: strings.SplitN(target, ":", 2)[0],
			PID:     p.PID,
			CWD:     p.CWD,
			Command: p.Command,
		})
	})
	return panes
}

func (a *Adapter) MatchInstances(procs []terminal.AgentProcess, panes []terminal.CandidatePane, _ map[int]int) []terminal.InstanceMatch {
	var matches []terminal.InstanceMatch
	matched := make(map[int]bool) // track matched process indices

	// Pass 1: match by CWD (parsed from surface titles)
	for _, proc := range procs {
		if proc.CWD == "" || matched[proc.Index] {
			continue
		}
		for _, pane := range panes {
			if pane.CWD != "" && pane.CWD == proc.CWD {
				matches = append(matches, terminal.InstanceMatch{
					ProcessIndex: proc.Index,
					Target:       pane.Target,
					CWD:          pane.CWD,
				})
				matched[proc.Index] = true
				break
			}
		}
	}

	// Pass 2: match by session/workspace name against project name
	for _, proc := range procs {
		if matched[proc.Index] {
			continue
		}
		for _, sess := range sessionsFromPanes(panes) {
			if sess == proc.Project {
				matches = append(matches, terminal.InstanceMatch{
					ProcessIndex: proc.Index,
					Target:       sess, // workspace name for CapturePane active-surface fallback
					CWD:          proc.CWD,
				})
				matched[proc.Index] = true
				break
			}
		}
	}

	return matches
}

// sessionsFromPanes extracts unique session names from candidate panes.
func sessionsFromPanes(panes []terminal.CandidatePane) []string {
	seen := make(map[string]bool)
	var names []string
	for _, p := range panes {
		if !seen[p.Session] {
			seen[p.Session] = true
			names = append(names, p.Session)
		}
	}
	return names
}
