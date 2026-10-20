package leaninteract

import (
	"log/slog"
	"os"
	"path/filepath"
)

const (
	// DefaultREPLGitURL is the Lean REPL repository used by default.
	DefaultREPLGitURL = "https://github.com/augustepoiroux/repl"
	// DefaultREPLVersion is the REPL revision used by default.
	DefaultREPLVersion = "v1.3.19"
)

// Logger receives the library's log output. Replace it to customize logging.
var Logger = slog.Default().With("lib", "lean_interact")

// DefaultCacheDir returns the cache directory: $LEAN_INTERACT_CACHE_DIR if
// set, otherwise <user cache dir>/lean_interact.
func DefaultCacheDir() string {
	if d := os.Getenv("LEAN_INTERACT_CACHE_DIR"); d != "" {
		return d
	}
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	return filepath.Join(base, "lean_interact")
}

// ClearCache removes the default cache directory.
func ClearCache() error {
	return os.RemoveAll(DefaultCacheDir())
}
