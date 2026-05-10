// Package cmd wires the tflens CLI via cobra. Each subcommand lives in
// its own file and registers itself with rootCmd in an init().
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/forcenew"
)

var rootCmd = &cobra.Command{
	Use:   "tflens",
	Short: "Parse, analyse, validate, and diff Terraform modules",
	Long: `tflens is a standalone Terraform/HCL parser and analysis tool.

It parses .tf files into an AST, builds a dependency graph, validates
references and types, and diffs two module versions to surface breaking
changes. It does not execute Terraform and does not need provider
schemas.

By default, module calls whose source is a Terraform Registry address or
a git URL are fetched on demand (and cached for next time) so downstream
analysis can traverse into them. Pass --offline to disable network
fetches — local paths and .terraform/modules/modules.json entries are
still resolved.`,
	SilenceUsage:  true,
	SilenceErrors: true,
	// PersistentPreRunE runs before every subcommand so the force-new
	// override (if any) is loaded once at startup. forcenew.Init is a
	// no-op for an empty path; for a non-empty path it eagerly opens
	// and parses the override file, so a bad path fails the CLI early
	// rather than silently producing wrong classifications later.
	PersistentPreRunE: func(cmd *cobra.Command, args []string) error {
		override, _ := cmd.Flags().GetString("immutable-table-override")
		return forcenew.Init(override)
	},
}

func init() {
	rootCmd.PersistentFlags().String("format", "text",
		"output format: text or json. When json, structured output goes to stdout; warnings stay on stderr")
	rootCmd.PersistentFlags().Bool("offline", false,
		"disable registry and git fetches; only local paths and .terraform/modules/modules.json are resolved")
	rootCmd.PersistentFlags().String("provider-schema", "",
		"path to a `terraform providers schema -json` output file; enables resource-attribute validation and richer type inference (omit for schema-less behaviour)")
	rootCmd.PersistentFlags().String("immutable-table-override", "",
		"path to a JSON force-new table to merge over the embedded one (use `tflens refresh-force-new` to fetch from a Crossplane runtime IR)")
}

// Execute runs the CLI. It is called from main().
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}
