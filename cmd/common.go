package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/loader"
)

// loadOldAndNew loads both sides of a project diff (the working tree
// at path + the same path materialised in a temporary git worktree at
// baseRef) using the standard resolver chain. Reads --offline from
// the command flags. Thin cobra-side wrapper around
// loader.LoadProjectsForDiff. The returned cleanup is non-nil even
// on the error paths so a deferred cleanup() is always safe.
func loadOldAndNew(cmd *cobra.Command, path, baseRef string) (oldProj, newProj *loader.Project, cleanup func(), err error) {
	offline, _ := cmd.Flags().GetBool("offline")
	return loader.LoadProjectsForDiff(path, baseRef, offline)
}

// mustLoadModule loads a single .tf file or a directory of .tf files
// via loader.LoadAny. File-level parse errors are printed as warnings
// to stderr; a top-level I/O failure (missing path, unreadable inode)
// is fatal. The returned module may be nil only when LoadAny itself
// errored — file-level partial-parse results still produce a usable
// module.
func mustLoadModule(path string) *analysis.Module {
	mod, fileErrs, err := loader.LoadAny(path)
	if err != nil {
		fatalf("%v", err)
	}
	printFileErrs(fileErrs)
	if mod == nil {
		os.Exit(1)
	}
	return mod
}

func plural(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
