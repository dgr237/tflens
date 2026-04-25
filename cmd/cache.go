package cmd

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/cache"
	"github.com/dgr237/tflens/pkg/config"
)

var cacheCmd = &cobra.Command{
	Use:   "cache",
	Short: "Manage the module-resolution cache",
	Long: `The cache stores modules downloaded from the Terraform Registry and
from git during online-mode resolution. Its location is the OS user
cache directory plus tflens/modules (e.g. ~/.cache/tflens/modules on
Linux, %LocalAppData%\tflens\modules on Windows).

Entries are immutable and keyed by content identity (host, path,
concrete version), so a given version is only fetched once. This
command lets you inspect and, if needed, clear the cache.`,
}

var cacheInfoCmd = &cobra.Command{
	Use:   "info",
	Short: "Show the cache location, entry count, and total size",
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := cache.Default()
		if err != nil {
			return err
		}
		entries, bytes, err := cacheStats(c.Root())
		if err != nil {
			return err
		}
		if config.FromCommand(cmd).JSON {
			exitJSON(struct {
				Path    string `json:"path"`
				Entries int    `json:"entries"`
				Bytes   int64  `json:"bytes"`
			}{c.Root(), entries, bytes}, 0)
			return nil
		}
		fmt.Printf("Path:    %s\n", c.Root())
		fmt.Printf("Entries: %d\n", entries)
		fmt.Printf("Size:    %s\n", humanBytes(bytes))
		return nil
	},
}

var cacheClearCmd = &cobra.Command{
	Use:   "clear",
	Short: "Delete every cached module",
	Long: `Remove the entire tflens cache directory. Subsequent online-mode
resolves will re-download. Use this when you've run out of disk, or
when you suspect a cached entry has been corrupted (since the cache
trusts its own contents as immutable, re-fetching requires clearing).`,
	RunE: func(cmd *cobra.Command, args []string) error {
		c, err := cache.Default()
		if err != nil {
			return err
		}
		root := c.Root()
		info, err := os.Stat(root)
		if err != nil {
			if os.IsNotExist(err) {
				fmt.Printf("Cache is already empty (%s does not exist).\n", root)
				return nil
			}
			return err
		}
		if !info.IsDir() {
			return fmt.Errorf("cache path %s is not a directory", root)
		}
		entries, bytes, _ := cacheStats(root)
		if err := os.RemoveAll(root); err != nil {
			return fmt.Errorf("removing cache: %w", err)
		}
		fmt.Printf("Cleared %d entries (%s) from %s.\n", entries, humanBytes(bytes), root)
		return nil
	},
}

func init() {
	cacheCmd.AddCommand(cacheInfoCmd)
	cacheCmd.AddCommand(cacheClearCmd)
	rootCmd.AddCommand(cacheCmd)
}

// cacheStats walks root and returns the number of leaf directories that
// look like cache entries (any directory that contains a file at any
// depth) and the total bytes of all regular files. A non-existent root
// returns (0, 0, nil) rather than an error so `info` prints zeroes
// cleanly before the first online resolve.
func cacheStats(root string) (entries int, bytes int64, err error) {
	if _, err := os.Stat(root); os.IsNotExist(err) {
		return 0, 0, nil
	} else if err != nil {
		return 0, 0, err
	}

	// Count entries as the number of leaf-ish directories: those that
	// contain a regular file directly. This matches how Put produces
	// entries — one directory per (host, path, version) with module
	// files inside. It slightly over-counts nested dirs-with-files
	// (e.g. a git clone's modules/child) but that's a sensible proxy
	// for "how many modules am I holding."
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			ents, e := os.ReadDir(path)
			if e == nil {
				for _, ent := range ents {
					if !ent.IsDir() {
						entries++
						break
					}
				}
			}
			return nil
		}
		info, e := d.Info()
		if e != nil {
			return nil
		}
		bytes += info.Size()
		return nil
	})
	return entries, bytes, err
}

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
