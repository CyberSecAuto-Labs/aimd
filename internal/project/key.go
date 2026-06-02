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
//  2. Strip user info ('user@') only when the '@' is in the authority
//     (before the first '/'); an '@' inside the path never truncates the host
//  3. Drop an explicit SSH port (':<digits>'); otherwise convert the scp ':'
//     host:path separator to '/'
//  4. Strip .git suffix
//  5. Convert '/' to '~' for filesystem safety
//  6. Lowercase the result
//
// The port handling makes keys stable: ssh://git@host:22/org/repo.git and
// git@host:org/repo.git both derive to "host~org~repo".
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

	// Step 2: Strip user info ('user@') only when '@' occurs in the authority
	// (before the first '/'). An '@' in the path must not truncate the host.
	s = stripUserinfo(s)

	// Step 3: Drop an explicit SSH port or convert the scp ':' separator.
	s = handlePortAndScpSeparator(s)

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

// stripUserinfo removes a leading 'user@' when the '@' appears in the authority
// (before the first '/'). An '@' that occurs in the path is left untouched so
// it cannot truncate the host.
func stripUserinfo(s string) string {
	at := strings.Index(s, "@")
	if at == -1 {
		return s
	}
	slash := strings.Index(s, "/")
	if slash != -1 && at > slash {
		// The '@' is in the path, not the authority — leave it alone.
		return s
	}
	return s[at+1:]
}

// handlePortAndScpSeparator handles the first ':' in the authority. If the
// segment after it (up to the next '/') is all digits, it is an explicit port
// and is dropped entirely so the port never appears in the key. Otherwise the
// ':' is the scp host:path separator and is converted to '/'.
func handlePortAndScpSeparator(s string) string {
	colon := strings.Index(s, ":")
	if colon == -1 {
		return s
	}
	rest := s[colon+1:]
	end := strings.Index(rest, "/")
	var seg, tail string
	if end == -1 {
		seg = rest
		tail = ""
	} else {
		seg = rest[:end]
		tail = rest[end:] // includes the leading '/'
	}

	if seg != "" && isAllDigits(seg) {
		// Explicit port: drop ':<digits>' entirely.
		return s[:colon] + tail
	}

	// scp host:path separator: convert the first ':' to '/'.
	return s[:colon] + "/" + s[colon+1:]
}

// isAllDigits reports whether s is non-empty and consists only of ASCII digits.
func isAllDigits(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
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
