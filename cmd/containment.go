package cmd

import (
	"os"
	"strings"
)

// pathEscapesRoot reports whether relPath climbs above its root with "..".
// Commands that join a relative path onto a trusted root call this before any
// filesystem mutation so a "../" target can never reach outside that root.
func pathEscapesRoot(relPath string) bool {
	return relPath == ".." || strings.HasPrefix(relPath, ".."+string(os.PathSeparator))
}
