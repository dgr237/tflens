package cmd

import (
	"github.com/dgr237/tflens/pkg/loader"
)

// RefAutoKeyword is the user-facing keyword that triggers base-ref
// auto-detection, e.g. `tflens diff --ref auto`. Chosen over pflag's
// NoOptDefVal because that would make `--ref main <ws>` parse as
// `--ref=<auto>` plus a positional `main` — worse UX than an
// explicit keyword.
const RefAutoKeyword = "auto"

// resolveAutoRef is a thin cmd-side wrapper around
// loader.ResolveAutoRef. Kept here so subcommand RunE callbacks have
// a one-call entry point that matches the rest of the cmd-layer's
// naming.
func resolveAutoRef(workspace string) (string, error) {
	return loader.ResolveAutoRef(workspace)
}
