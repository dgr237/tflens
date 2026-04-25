package render

import (
	"fmt"
	"io"
)

// writeCacheInfo emits the cache info subcommand's text result:
// path / entry count / total size, padded for column alignment.
func writeCacheInfo(w io.Writer, path string, entries int, bytes int64) {
	fmt.Fprintf(w, "Path:    %s\n", path)
	fmt.Fprintf(w, "Entries: %d\n", entries)
	fmt.Fprintf(w, "Size:    %s\n", humanBytes(bytes))
}

// writeCacheAlreadyEmpty emits the cache clear subcommand's "no-op"
// result — the cache directory doesn't exist yet, so there's nothing
// to clear.
func writeCacheAlreadyEmpty(w io.Writer, path string) {
	fmt.Fprintf(w, "Cache is already empty (%s does not exist).\n", path)
}

// writeCacheCleared emits the cache clear subcommand's success line:
// how many entries (and bytes) were removed and the cache path.
func writeCacheCleared(w io.Writer, entries int, bytes int64, path string) {
	fmt.Fprintf(w, "Cleared %d entries (%s) from %s.\n", entries, humanBytes(bytes), path)
}

// humanBytes formats a byte count as a short power-of-1024 string
// (e.g. 4096 → "4.0 KiB"). Exposed for cache-related callers and the
// info/clear subcommands.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for x := n / unit; x >= unit; x /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}
