package gitlocal

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Repo represents a local git repository.
type Repo struct {
	Name          string `json:"name"`          // directory name
	Path          string `json:"path"`          // absolute path
	RemoteURL     string `json:"remoteUrl"`     // origin remote URL
	DefaultBranch string `json:"defaultBranch"` // e.g. "main"
}

// Commit represents a commit from local git log.
type Commit struct {
	SHA         string    `json:"sha"`
	Author      string    `json:"author"`
	Message     string    `json:"message"`
	CommittedAt time.Time `json:"committedAt"`
	URL         string    `json:"url"` // derived from remote URL if possible
}

// CommitFile represents a file changed in a commit.
type CommitFile struct {
	Filename  string `json:"filename"`
	Status    string `json:"status"` // added, modified, removed, renamed
	Additions int    `json:"additions"`
	Deletions int    `json:"deletions"`
}

// CodeDir returns the root code directory from CMDR_CODE_DIR env var,
// defaulting to ~/Code.
func CodeDir() string {
	if dir := os.Getenv("CMDR_CODE_DIR"); dir != "" {
		return expandHome(dir)
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Code")
}

// Discover scans codeDir for git repos with remotes.
// Goes two levels deep: if a first-level dir is not a git repo,
// checks its children (e.g. ~/Code/minicart/api).
func Discover(codeDir string) ([]Repo, error) {
	entries, err := os.ReadDir(codeDir)
	if err != nil {
		return nil, fmt.Errorf("gitlocal: read code dir %s: %w", codeDir, err)
	}

	var repos []Repo
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}

		dirPath := filepath.Join(codeDir, e.Name())

		if repo, ok := tryRepo(dirPath, e.Name()); ok {
			repos = append(repos, repo)
			continue
		}

		// Not a git repo — check one level deeper (namespace folder)
		subEntries, err := os.ReadDir(dirPath)
		if err != nil {
			continue
		}
		for _, sub := range subEntries {
			if !sub.IsDir() || strings.HasPrefix(sub.Name(), ".") {
				continue
			}
			subPath := filepath.Join(dirPath, sub.Name())
			// Use "namespace/repo" as the display name
			name := e.Name() + "/" + sub.Name()
			if repo, ok := tryRepo(subPath, name); ok {
				repos = append(repos, repo)
			}
		}
	}
	return repos, nil
}

// tryRepo checks if a directory is a git repo with a remote and returns it.
func tryRepo(path, name string) (Repo, bool) {
	gitDir := filepath.Join(path, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return Repo{}, false
	}

	remoteURL, err := gitOutput(path, "remote", "get-url", "origin")
	if err != nil || remoteURL == "" {
		return Repo{}, false
	}

	return Repo{
		Name:          name,
		Path:          path,
		RemoteURL:     remoteURL,
		DefaultBranch: defaultBranch(path),
	}, true
}

// Fetch runs git fetch for a repo.
func Fetch(repoPath string) error {
	cmd := exec.Command("git", "-C", repoPath, "fetch", "--all", "--prune", "-q")
	cmd.Stderr = nil
	return cmd.Run()
}

// Log returns recent commits on the remote tracking branch.
// Always fetches the last `limit` commits and relies on INSERT OR IGNORE
// for dedup. Using --since is unreliable because author dates can differ
// from push times (rebases, late pushes).
func Log(repoPath, branch string, limit int) ([]Commit, error) {
	ref := fmt.Sprintf("origin/%s", branch)

	out, err := gitOutput(repoPath, "log", ref,
		"--format=%H%x00%an%x00%s%x00%aI",
		fmt.Sprintf("--max-count=%d", limit),
	)
	if err != nil {
		return nil, fmt.Errorf("gitlocal: log %s: %w", repoPath, err)
	}

	remoteURL := remoteOriginURL(repoPath)

	var commits []Commit
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\x00", 4)
		if len(parts) < 4 {
			continue
		}
		t, _ := time.Parse(time.RFC3339, parts[3])
		commits = append(commits, Commit{
			SHA:         parts[0],
			Author:      parts[1],
			Message:     parts[2],
			CommittedAt: t,
			URL:         commitURL(remoteURL, parts[0]),
		})
	}
	return commits, nil
}

// CommitFiles returns the list of files changed in a commit.
func CommitFiles(repoPath, sha string) ([]CommitFile, error) {
	out, err := gitOutput(repoPath, "diff-tree", "--no-commit-id", "-r", "--numstat", sha)
	if err != nil {
		return nil, fmt.Errorf("gitlocal: diff-tree %s: %w", sha, err)
	}

	// Also get file statuses (A/M/D/R)
	statusOut, _ := gitOutput(repoPath, "diff-tree", "--no-commit-id", "-r", "--name-status", sha)
	statusMap := make(map[string]string)
	for _, line := range strings.Split(statusOut, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 2)
		if len(parts) == 2 {
			status := strings.ToLower(parts[0][:1])
			switch status {
			case "a":
				statusMap[parts[1]] = "added"
			case "d":
				statusMap[parts[1]] = "removed"
			case "r":
				statusMap[parts[1]] = "renamed"
			default:
				statusMap[parts[1]] = "modified"
			}
		}
	}

	var files []CommitFile
	for _, line := range strings.Split(out, "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) < 3 {
			continue
		}
		add, del := 0, 0
		fmt.Sscanf(parts[0], "%d", &add)
		fmt.Sscanf(parts[1], "%d", &del)

		status := statusMap[parts[2]]
		if status == "" {
			status = "modified"
		}

		files = append(files, CommitFile{
			Filename:  parts[2],
			Status:    status,
			Additions: add,
			Deletions: del,
		})
	}
	return files, nil
}

// DiffResult contains the diff content and its format.
type DiffResult struct {
	Diff   string   `json:"diff"`
	Format string   `json:"format"` // "delta" (HTML) or "unified" (plain text)
	Files  []string `json:"files"`  // list of changed file paths
}

// extractDiffFiles parses file paths from a unified diff or delta HTML output.
func extractDiffFiles(diff, format string) []string {
	var files []string
	seen := make(map[string]bool)

	var pattern string
	if format == "delta" {
		// In delta HTML, file headers appear as lines starting with "diff --git"
		// but they're HTML-escaped. Look for the plain text before ANSI conversion.
		// Actually, after ANSI→HTML, the diff --git lines are still in the output.
		pattern = "diff --git a/"
	} else {
		pattern = "diff --git a/"
	}

	for _, line := range strings.Split(diff, "\n") {
		// Strip HTML tags for delta format
		clean := line
		if format == "delta" {
			clean = stripHTMLTags(clean)
		}
		if strings.HasPrefix(clean, pattern) {
			// "diff --git a/foo/bar.js b/foo/bar.js" → "foo/bar.js"
			parts := strings.SplitN(clean, " b/", 2)
			if len(parts) == 2 {
				file := strings.TrimSpace(parts[1])
				if !seen[file] {
					files = append(files, file)
					seen[file] = true
				}
			}
		}
	}
	return files
}

func stripHTMLTags(s string) string {
	var b strings.Builder
	inTag := false
	for _, r := range s {
		if r == '<' {
			inTag = true
		} else if r == '>' {
			inTag = false
		} else if !inTag {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// CommitDiff returns the diff for a commit.
// Tries difft first (syntax-aware, returns HTML), falls back to unified diff.
func CommitDiff(repoPath, sha string) (DiffResult, error) {
	// Try delta: word-level syntax highlighting on unified diff
	if _, err := exec.LookPath("delta"); err == nil {
		shellCmd := fmt.Sprintf(
			`git -C %q show --format= --patch --color=never %s | delta --color-only --paging=never --no-gitconfig --line-numbers --syntax-theme=rose-pine`,
			repoPath, sha,
		)
		cmd := exec.Command("sh", "-c", shellCmd)
		cmd.Env = append(os.Environ(), "TERM=dumb")
		out, err := cmd.Output()
		if err == nil && len(out) > 0 {
			html := AnsiToHTML(string(out))
			files := extractDiffFiles(html, "delta")
			html = injectFileAnchors(html, "delta")
			return DiffResult{
				Diff:   html,
				Format: "delta",
				Files:  files,
			}, nil
		}
	}

	// Fallback: plain unified diff
	out, err := gitOutput(repoPath, "show", "--format=", "--patch", sha)
	if err != nil {
		return DiffResult{}, fmt.Errorf("gitlocal: diff %s: %w", sha, err)
	}
	diff := out
	files := extractDiffFiles(diff, "unified")
	return DiffResult{Diff: diff, Format: "unified", Files: files}, nil
}

// injectFileAnchors adds id attributes to diff file headers for anchor scrolling.
func injectFileAnchors(diff, format string) string {
	fileIdx := 0
	files := extractDiffFiles(diff, format)
	if len(files) == 0 {
		return diff
	}

	var result strings.Builder
	for _, line := range strings.Split(diff, "\n") {
		clean := line
		if format == "delta" {
			clean = stripHTMLTags(clean)
		}
		if strings.HasPrefix(clean, "diff --git a/") && fileIdx < len(files) {
			anchor := fmt.Sprintf(`<span id="file-%d"></span>`, fileIdx)
			result.WriteString(anchor)
			fileIdx++
		}
		result.WriteString(line)
		result.WriteByte('\n')
	}
	return result.String()
}

// --- helpers ---

func gitOutput(repoPath string, args ...string) (string, error) {
	fullArgs := append([]string{"-C", repoPath}, args...)
	out, err := exec.Command("git", fullArgs...).Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func defaultBranch(repoPath string) string {
	// Try symbolic-ref of origin HEAD
	ref, err := gitOutput(repoPath, "symbolic-ref", "refs/remotes/origin/HEAD")
	if err == nil && ref != "" {
		// refs/remotes/origin/main → main
		parts := strings.Split(ref, "/")
		return parts[len(parts)-1]
	}
	// Fallback: check if main exists, else master, else main
	for _, b := range []string{"main", "master"} {
		if _, err := gitOutput(repoPath, "rev-parse", "--verify", "origin/"+b); err == nil {
			return b
		}
	}
	return "main"
}

func remoteOriginURL(repoPath string) string {
	url, _ := gitOutput(repoPath, "remote", "get-url", "origin")
	return url
}

// RepoSlug returns the "owner/name" portion of the repo's GitHub origin URL,
// or "" if the remote isn't a recognizable GitHub URL.
func RepoSlug(repoPath string) string {
	raw := remoteOriginURL(repoPath)
	if raw == "" {
		return ""
	}
	// SSH form (git@github.com:owner/repo.git) → https form
	if strings.HasPrefix(raw, "git@") {
		raw = strings.TrimPrefix(raw, "git@")
		raw = strings.Replace(raw, ":", "/", 1)
		raw = "https://" + raw
	}
	raw = strings.TrimSuffix(raw, ".git")
	const prefix = "https://github.com/"
	if !strings.HasPrefix(raw, prefix) {
		return ""
	}
	return strings.TrimPrefix(raw, prefix)
}

// commitURL derives a web URL for a commit from the remote URL.
func commitURL(remoteURL, sha string) string {
	// Handle SSH: git@github.com:owner/repo.git
	if strings.HasPrefix(remoteURL, "git@") {
		remoteURL = strings.TrimPrefix(remoteURL, "git@")
		remoteURL = strings.Replace(remoteURL, ":", "/", 1)
		remoteURL = "https://" + remoteURL
	}
	remoteURL = strings.TrimSuffix(remoteURL, ".git")
	if strings.Contains(remoteURL, "github.com") || strings.Contains(remoteURL, "gitlab.com") {
		return remoteURL + "/commit/" + sha
	}
	return ""
}

func expandHome(path string) string {
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		return filepath.Join(home, path[2:])
	}
	return path
}
