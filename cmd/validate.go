package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/render"
)

var validateCmd = &cobra.Command{
	Use:   "validate <path>",
	Short: "Report undefined references, type errors, and cross-module input issues",
	Long: `Validate runs several static checks:
  - undefined var.*, local.*, module.*, data.*.* references
  - variable default value type mismatches
  - for_each / count meta-argument misuse
  - outputs leaking sensitive variables without being themselves sensitive
  - cross-module inputs: missing required args, unknown args, type mismatches

When <path> is a directory, the workspace is loaded as a project and any
local submodules (including those resolved via .terraform/modules/modules.json
after 'terraform init') are cross-validated.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runValidate(cmd, args[0])
	},
}

func init() {
	rootCmd.AddCommand(validateCmd)
}

func runValidate(cmd *cobra.Command, path string) {
	mod, crossErrs := loadForValidate(cmd, path)
	refErrs := mod.Validate()
	typeErrs := mod.TypeErrors()
	total := len(refErrs) + len(typeErrs) + len(crossErrs)

	if outputJSON(cmd) {
		refJSON := make([]render.JSONValidationError, 0, len(refErrs))
		crossJSON := make([]render.JSONValidationError, 0, len(crossErrs))
		typeJSON := make([]render.JSONTypeError, 0, len(typeErrs))
		// Refs and cross-errors both reuse ValidationError but have different
		// semantics. Keep them separate for consumers.
		for _, e := range refErrs {
			refJSON = append(refJSON, render.JSONValErr(e))
		}
		for _, e := range crossErrs {
			crossJSON = append(crossJSON, render.JSONValErr(e))
		}
		for _, e := range typeErrs {
			typeJSON = append(typeJSON, render.JSONTypeErr(e))
		}
		code := 0
		if total > 0 {
			code = 1
		}
		exitJSON(struct {
			UndefinedReferences []render.JSONValidationError `json:"undefined_references"`
			CrossModuleIssues   []render.JSONValidationError `json:"cross_module_issues"`
			TypeErrors          []render.JSONTypeError       `json:"type_errors"`
		}{refJSON, crossJSON, typeJSON}, code)
		return
	}

	if total == 0 {
		fmt.Println("No validation errors found.")
		return
	}
	section := func(sep bool, title string, items []analysis.ValidationError) bool {
		if len(items) == 0 {
			return sep
		}
		if sep {
			fmt.Fprintln(os.Stderr)
		}
		fmt.Fprintf(os.Stderr, "%s (%d):\n", title, len(items))
		for _, e := range items {
			fmt.Fprintf(os.Stderr, "  %s\n", e)
		}
		return true
	}
	sep := section(false, "Undefined references", refErrs)
	sep = section(sep, "Cross-module issues", crossErrs)
	if len(typeErrs) > 0 {
		if sep {
			fmt.Fprintln(os.Stderr)
		}
		fmt.Fprintf(os.Stderr, "Type errors (%d):\n", len(typeErrs))
		for _, e := range typeErrs {
			fmt.Fprintf(os.Stderr, "  %s\n", e)
		}
	}
	os.Exit(1)
}

// loadForValidate returns the root module for validation plus any
// cross-module errors discovered by walking into locally-referenced
// child modules. Thin cobra-side wrapper around loader.LoadForValidate;
// fatal on I/O errors and prints any FileErrors to stderr as warnings.
func loadForValidate(cmd *cobra.Command, path string) (*analysis.Module, []analysis.ValidationError) {
	offline, _ := cmd.Flags().GetBool("offline")
	mod, crossErrs, fileErrs, err := loader.LoadForValidate(path, offline)
	if err != nil {
		fatalf("%v", err)
	}
	printFileErrs(fileErrs)
	return mod, crossErrs
}
