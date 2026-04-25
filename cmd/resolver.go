package cmd

import (
	"fmt"
	"os"

	"github.com/dgr237/tflens/pkg/loader"
)

// printFileErrs writes any non-fatal parse warnings to stderr.
// Centralised here so every cmd-side caller of a loader.LoadXxx
// function uses the same format.
func printFileErrs(fileErrs []loader.FileError) {
	for _, fe := range fileErrs {
		fmt.Fprintf(os.Stderr, "warning: parse errors in %s\n", fe.Path)
		for _, e := range fe.Errors {
			fmt.Fprintf(os.Stderr, "  %s\n", e)
		}
	}
}
