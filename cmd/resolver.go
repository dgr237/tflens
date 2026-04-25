package cmd

import (
	"fmt"

	"github.com/dgr237/tflens/pkg/config"
	"github.com/dgr237/tflens/pkg/loader"
)

// printFileErrs writes any non-fatal parse warnings to s.Err.
// Centralised here so every cmd-side caller of a loader.LoadXxx
// function uses the same format.
func printFileErrs(s config.Settings, fileErrs []loader.FileError) {
	for _, fe := range fileErrs {
		fmt.Fprintf(s.Err, "warning: parse errors in %s\n", fe.Path)
		for _, e := range fe.Errors {
			fmt.Fprintf(s.Err, "  %s\n", e)
		}
	}
}
