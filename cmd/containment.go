package cmd

import (
	"os"
	"path/filepath"
	"strings"
)

// pathEscapesRoot reports whether relPath climbs above its root with "..".
// Commands that join a relative path onto a trusted root call this before any
// filesystem mutation so a "../" target can never reach outside that root. The
// path is normalised first so an embedded "foo/../.." from an untrusted source
// is caught too.
func pathEscapesRoot(relPath string) bool {
	relPath = filepath.Clean(relPath)
	return relPath == ".." || strings.HasPrefix(relPath, ".."+string(os.PathSeparator))
}
