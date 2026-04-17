package terminal

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// EditorBin returns the configured editor command.
// Reads CMDR_EDITOR env var, defaults to "nvim".
func EditorBin() string {
	if e := os.Getenv("CMDR_EDITOR"); e != "" {
		return e
	}
	return "nvim"
}

// IsEditorProcess checks if a process name matches the configured editor.
func IsEditorProcess(command string) bool {
	editor := filepath.Base(EditorBin())
	base := filepath.Base(command)
	return base == editor || (editor == "nvim" && base == "vim")
}

// IsVimLike returns true if the configured editor supports :e commands.
func IsVimLike() bool {
	editor := filepath.Base(EditorBin())
	return editor == "nvim" || editor == "vim"
}

// EditorOpenCmd returns the shell command to launch the editor at file:line.
func EditorOpenCmd(file string, line int) string {
	return fmt.Sprintf("exec %s +%d %s", EditorBin(), line, file)
}

// SendEditorOpen sends the appropriate command to an existing editor pane
// to open a file. For vim/nvim: Esc + ":e +line file". For others: sends
// the editor CLI command as text.
func SendEditorOpen(mux Multiplexer, target, file string, line int) error {
	if IsVimLike() {
		if err := mux.SendKeys(target, "Escape", false); err != nil {
			return err
		}
		time.Sleep(50 * time.Millisecond)
		cmd := fmt.Sprintf(":e +%d %s", line, file)
		return mux.SendKeys(target, cmd, true)
	}
	cmd := strings.Join([]string{EditorBin(), fmt.Sprintf("+%d", line), file}, " ")
	return mux.SendKeys(target, cmd, true)
}

// ResolveRepoPath resolves symlinks and cleans a repo path for comparison.
func ResolveRepoPath(repoPath string) string {
	if resolved, err := filepath.EvalSymlinks(repoPath); err == nil {
		repoPath = resolved
	}
	return filepath.Clean(repoPath)
}

// SessionMatchesRepo checks if any pane in the session has a CWD matching repoPath.
func SessionMatchesRepo(s Session, repoPath string) bool {
	for _, w := range s.Windows {
		for _, p := range w.Panes {
			if filepath.Clean(p.CWD) == repoPath {
				return true
			}
		}
	}
	return false
}
