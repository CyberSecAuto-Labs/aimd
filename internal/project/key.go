package project

import (
	"crypto/sha256"
	"fmt"
	"path/filepath"
	"strings"
)

// DeriveKey normalizes a git remote URL to a filesystem-safe project key.
//
// Normalization rules (applied in order):
//  1. Strip protocol prefix: git@, https://, http://, ssh://
//  2. Strip user info: user@
//  3. Convert ':' SSH separator to '/'
//  4. Strip .git suffix
//  5. Convert '/' to '~' for filesystem safety
//  6. Lowercase the result
func DeriveKey(remoteURL string) (string, error) {
	if remoteURL == "" {
		return "", fmt.Errorf("remote URL is empty")
	}

	s := remoteURL

	// Step 1: Strip protocol prefixes.
	for _, prefix := range []string{"ssh://", "https://", "http://", "git@"} {
		if strings.HasPrefix(s, prefix) {
			s = strings.TrimPrefix(s, prefix)
			break
		}
	}

	// Step 2: Strip user info (anything before '@' that wasn't already removed).
	if idx := strings.Index(s, "@"); idx != -1 {
		s = s[idx+1:]
	}

	// Step 3: Convert ':' SSH separator to '/'.
	// Only replace the first ':' to handle host:path form.
	if idx := strings.Index(s, ":"); idx != -1 {
		s = s[:idx] + "/" + s[idx+1:]
	}

	// Step 4: Strip .git suffix.
	s = strings.TrimSuffix(s, ".git")

	// Step 5: Convert '/' to '~'.
	s = strings.ReplaceAll(s, "/", "~")

	// Step 6: Lowercase.
	s = strings.ToLower(s)

	if s == "" {
		return "", fmt.Errorf("could not derive key from remote URL: %q", remoteURL)
	}

	return s, nil
}

// DeriveKeyFromPath returns a filesystem-safe project key for repositories with
// no remote. Format: "local~" + sha256(absPath)[:8] + "~" + filepath.Base(absPath),
// all lowercase.
func DeriveKeyFromPath(absPath string) string {
	hash := sha256.Sum256([]byte(absPath))
	shortHash := fmt.Sprintf("%x", hash[:4]) // 4 bytes = 8 hex chars
	base := strings.ToLower(filepath.Base(absPath))
	return "local~" + shortHash + "~" + base
}
