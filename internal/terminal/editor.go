package terminal

import (
	"fmt"
	"path/filepath"
	"time"
)

// FindOrCreateEditor locates an editor pane (nvim/vim) in a session whose
// working directory matches repoPath. If no matching session exists, one is
// created. If the session exists but has no editor pane, a new window is
// created with the editor opened to file+line.
func FindOrCreateEditor(mux Multiplexer, repoPath, file string, line int) (*EditorTarget, error) {
	sessions, err := mux.ListSessions()
	if err != nil {
		return nil, err
	}

	// Resolve symlinks so paths match
	if resolved, err := filepath.EvalSymlinks(repoPath); err == nil {
		repoPath = resolved
	}
	repoPath = filepath.Clean(repoPath)

	// First pass: find an existing editor pane in a session matching the repo
	for _, s := range sessions {
		if !sessionMatchesRepo(s, repoPath) {
			continue
		}
		for _, w := range s.Windows {
			for _, p := range w.Panes {
				if p.Command == "nvim" || p.Command == "vim" {
					target := fmt.Sprintf("%s:%d.%d", s.Name, w.Index, p.Index)
					return &EditorTarget{Session: s.Name, Target: target, Fresh: false}, nil
				}
			}
		}
		// Session exists but no editor pane — create one with the file
		target, err := createEditorWindow(mux, s.Name, repoPath, file, line)
		if err != nil {
			return nil, err
		}
		return &EditorTarget{Session: s.Name, Target: target, Fresh: true}, nil
	}

	// No matching session — create one, then open the editor
	sessName, err := mux.CreateSession(repoPath)
	if err != nil {
		return nil, fmt.Errorf("creating session for %s: %w", repoPath, err)
	}

	target, err := createEditorWindow(mux, sessName, repoPath, file, line)
	if err != nil {
		return nil, fmt.Errorf("creating editor window: %w", err)
	}

	return &EditorTarget{Session: sessName, Target: target, Fresh: true}, nil
}

// OpenFileInEditor sends a command to an existing editor pane to open a file.
func OpenFileInEditor(mux Multiplexer, target, file string, line int) error {
	// Esc ensures we're in normal mode
	if err := mux.SendKeys(target, "Escape", false); err != nil {
		return err
	}
	time.Sleep(50 * time.Millisecond)
	cmd := fmt.Sprintf(":e +%d %s", line, file)
	return mux.SendKeys(target, cmd, true)
}

func sessionMatchesRepo(s Session, repoPath string) bool {
	for _, w := range s.Windows {
		for _, p := range w.Panes {
			if filepath.Clean(p.CWD) == repoPath {
				return true
			}
		}
	}
	return false
}

func createEditorWindow(mux Multiplexer, sessionName, dir, file string, line int) (string, error) {
	nvimCmd := fmt.Sprintf("exec nvim +%d %s", line, file)
	return mux.CreateWindow(sessionName, "editor", dir, nvimCmd)
}
