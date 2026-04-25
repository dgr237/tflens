package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/loader"
)

// loadProject is the CLI's one-stop loader: reads --offline from the
// command flags, calls loader.LoadProjectDefaults, and prints any
// collected FileErrors to stderr as warnings. The returned project
// is always non-nil on a nil error.
func loadProject(cmd *cobra.Command, rootDir string) (*loader.Project, error) {
	offline, _ := cmd.Flags().GetBool("offline")
	project, fileErrs, err := loader.LoadProjectDefaults(rootDir, offline)
	if err != nil {
		return nil, err
	}
	printFileErrs(fileErrs)
	return project, nil
}

func printFileErrs(fileErrs []loader.FileError) {
	for _, fe := range fileErrs {
		fmt.Fprintf(os.Stderr, "warning: parse errors in %s\n", fe.Path)
		for _, e := range fe.Errors {
			fmt.Fprintf(os.Stderr, "  %s\n", e)
		}
	}
}
