package gitlocal

import (
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

// diffCacheKey identifies one cached diff result.
type diffCacheKey struct {
	repoPath  string
	anchorSHA string
	headSHA   string
}

var (
	diffCacheMu sync.Mutex
	diffCache   = map[diffCacheKey][]string{}
)

// ChangedFilesSince returns the list of files changed between anchorSHA
// and the repo's current HEAD. Results cache in-memory keyed by
// (repoPath, anchorSHA, headSHA); the cache invalidates naturally as
// HEAD moves (the key changes), so no manual eviction is needed.
//
// When anchorSHA == headSHA the diff is empty by definition; we
// short-circuit without invoking git.
func ChangedFilesSince(repoPath, anchorSHA string) ([]string, error) {
	headSHA, err := HeadSHA(repoPath)
	if err != nil {
		return nil, err
	}
	if anchorSHA == "" || anchorSHA == headSHA {
		return nil, nil
	}

	key := diffCacheKey{repoPath: repoPath, anchorSHA: anchorSHA, headSHA: headSHA}

	diffCacheMu.Lock()
	if files, ok := diffCache[key]; ok {
		diffCacheMu.Unlock()
		return files, nil
	}
	diffCacheMu.Unlock()

	out, err := exec.Command("git", "-C", repoPath,
		"diff", "--name-only", anchorSHA+"..HEAD",
	).Output()
	if err != nil {
		return nil, fmt.Errorf("git diff %s..HEAD: %w", anchorSHA, err)
	}

	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			files = append(files, line)
		}
	}

	diffCacheMu.Lock()
	diffCache[key] = files
	diffCacheMu.Unlock()
	return files, nil
}
