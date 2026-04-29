package graph

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"regexp"
	"strings"
)

// slugSafe matches characters allowed in the visible portion of a slug.
var slugSafe = regexp.MustCompile(`[^a-zA-Z0-9._-]+`)

// Slug derives a stable, filesystem-safe identifier for a repo path.
// Format: <basename>-<6-char-sha256-hex>. The hash makes slugs unique
// even when two repos share a basename (e.g. multiple "frontend" dirs).
func Slug(absRepoPath string) string {
	abs := filepath.Clean(absRepoPath)
	base := filepath.Base(abs)
	base = slugSafe.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		base = "repo"
	}
	sum := sha256.Sum256([]byte(abs))
	return base + "-" + hex.EncodeToString(sum[:])[:6]
}
