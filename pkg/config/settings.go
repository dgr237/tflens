// Package config carries the parsed shape of every flag tflens
// subcommands consume. The Settings struct is cobra-free so downstream
// helpers in pkg/loader, pkg/diff, pkg/render, etc. can take plain
// values rather than threading a *cobra.Command through every layer.
//
// The cobra dependency lives in FromCommand (cobra.go) and is the
// single point where flag-name strings are interpreted. Subcommands
// build a Settings at the top of their RunE and pass it (or just the
// fields they need) to the run* helpers.
package config

// RefAutoKeyword is the user-facing keyword that triggers base-ref
// auto-detection (e.g. `tflens diff --ref auto`). When BaseRef equals
// this value the RunE should call loader.ResolveAutoRef to find a
// concrete ref before continuing.
const RefAutoKeyword = "auto"

// Settings is the union of every flag (and a few positional args) that
// tflens subcommands read. Each subcommand only populates the fields
// relevant to it; flags not registered on the active command silently
// remain at their zero value.
//
// Construct with FromCommand for production use, or build a Settings
// literal directly for tests — the type is a plain struct with no
// cobra dependency.
type Settings struct {
	// Global flags

	// Offline disables registry + git resolvers; only local paths and
	// .terraform/modules/modules.json entries are followed.
	Offline bool
	// JSON is true when --format=json was given. Subcommands switch
	// from human-readable rendering to a structured envelope.
	JSON bool

	// Per-subcommand flags

	// BaseRef is the git ref to compare against (diff, whatif,
	// statediff). May be the literal RefAutoKeyword — the RunE is
	// responsible for resolving that to a concrete ref before passing
	// to loader.LoadProjectsForDiff.
	BaseRef string
	// StatePath is an optional Terraform state v4 JSON path
	// (statediff). Empty means "no state file supplied".
	StatePath string
	// Write makes fmt rewrite the input file in place.
	Write bool
	// Check makes fmt exit non-zero when the input is not already
	// formatted.
	Check bool

	// Positional / cmd-derived

	// Path is the workspace path the subcommand operates on. Defaulted
	// to "." by most subcommands when no positional arg is supplied.
	Path string
	// OnlyName scopes whatif to one module call by name. Empty means
	// "every changed call".
	OnlyName string
}
