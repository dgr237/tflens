// Package cmd wires the tflens CLI via cobra. Each subcommand lives in
// its own file and registers itself with rootCmd in an init().
package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "tflens",
	Short: "Parse, analyse, validate, and diff Terraform modules",
	Long: `tflens is a standalone Terraform/HCL parser and analysis tool.

It parses .tf files into an AST, builds a dependency graph, validates
references and types, and diffs two module versions to surface breaking
changes. It does not execute Terraform, does not need provider schemas,
and does not touch the network.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func init() {
	rootCmd.PersistentFlags().String("format", "text",
		"output format: text or json. When json, structured output goes to stdout; warnings stay on stderr")
}

// Execute runs the CLI. It is called from main().
func Execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// outputJSON reports whether the user asked for JSON output on this cmd.
func outputJSON(cmd *cobra.Command) bool {
	v, _ := cmd.Flags().GetString("format")
	return v == "json"
}
