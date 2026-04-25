// Package config carries the parsed shape of every flag tflens
// subcommands consume. The Settings struct is cobra-free so downstream
// helpers in pkg/loader, pkg/diff, pkg/render, etc. can take plain
// values rather than threading a *cobra.Command through every layer.
//
// The cobra dependency lives in FromCommand (cobra.go) and is the
// single point where flag-name strings are interpreted. Subcommands
// build a Settings at the top of their RunE — usually with one or two
// Option helpers (see options.go) — and pass it to the run* helpers.
package config

import "io"

// RefAutoKeyword is the user-facing keyword that triggers base-ref
// auto-detection (e.g. `tflens diff --ref auto`). When BaseRef equals
// this value the RunE should call loader.ResolveAutoRef to find a
// concrete ref before continuing.
const RefAutoKeyword = "auto"

// Settings is the union of every flag (and a few positional /
// cmd-derived values) that tflens subcommands read. Each subcommand
// only populates the fields relevant to it; flags not registered on
// the active command silently remain at their zero value.
//
// Construct with FromCommand for production use, or build a Settings
// literal directly for tests — the type is a plain struct with no
// cobra dependency.
type Settings struct {
	// Out is where rendered output goes (renderer-driven success
	// path; cobra's OutOrStdout). Defaults to os.Stdout.
	Out io.Writer
	// Err is where warnings + error sections go (FileError prints,
	// validate's "errors found" branch; cobra's ErrOrStderr).
	// Defaults to os.Stderr.
	Err io.Writer

	// Global flags

	// Offline disables registry + git resolvers; only local paths and
	// .terraform/modules/modules.json entries are followed.
	Offline bool
	// JSON is true when --format=json was given. Subcommands switch
	// from human-readable rendering to a structured envelope.
	JSON bool
	// Markdown is true when --format=markdown was given. The renderer
	// emits GitHub-flavoured markdown suitable for sticky-commenting on
	// a PR. Like JSON, the output is a single stream — warnings stay on
	// stdout rather than splitting to stderr — so the whole document
	// can be piped into `gh pr comment` or similar.
	Markdown bool

	// Per-subcommand flags

	// BaseRef is the git ref to compare against (diff, whatif,
	// statediff). May be the literal RefAutoKeyword — the RunE is
	// responsible for resolving that to a concrete ref before passing
	// to loader.LoadProjectsForDiff.
	BaseRef string
	// StatePath is an optional Terraform state v4 JSON path
	// (statediff). Empty means "no state file supplied".
	StatePath string
	// PlanPath is an optional terraform `show -json` plan output file
	// (diff --enrich-with-plan). When set, plan-derived attribute
	// deltas get folded into the diff result so resource attribute
	// changes (`cidr_block = "10.0.0.0/16"` → `"10.1.0.0/16"`) become
	// visible alongside the static-analysis findings. Empty means
	// "no plan supplied".
	PlanPath string
	// Write makes fmt rewrite the input file in place.
	Write bool
	// Check makes fmt exit non-zero when the input is not already
	// formatted.
	Check bool

	// Positional / cmd-derived (set via Option helpers in options.go)

	// Path is the workspace path the subcommand operates on. Defaulted
	// to "." by most subcommands when no positional arg is supplied.
	Path string
	// OnlyName scopes whatif to one module call by name. Empty means
	// "every changed call".
	OnlyName string
}
