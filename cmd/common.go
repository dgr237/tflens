package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/loader"
)

// loadOldAndNew loads both sides of a project diff: the working tree
// at path (the "new" side) and the same path at baseRef (the "old"
// side, materialised in a temporary git worktree). Returns the two
// projects plus a cleanup func the caller MUST defer to remove the
// worktree. Used by every subcommand that compares two refs.
func loadOldAndNew(cmd *cobra.Command, path, baseRef string) (oldProj, newProj *loader.Project, cleanup func(), err error) {
	newProj, err = loadProject(cmd, path)
	if err != nil {
		return nil, nil, func() {}, fmt.Errorf("loading path: %w", err)
	}
	oldProj, cleanup, err = loadOldProjectForRef(cmd, path, baseRef)
	if err != nil {
		return nil, nil, func() {}, err
	}
	return oldProj, newProj, cleanup, nil
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
	for _, fe := range fileErrs {
		fmt.Fprintf(os.Stderr, "warning: parse errors in %s\n", fe.Path)
		for _, e := range fe.Errors {
			fmt.Fprintf(os.Stderr, "  %s\n", e)
		}
	}
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
