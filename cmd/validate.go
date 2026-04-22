package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/loader"
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
	mod, crossErrs := loadForValidate(path)
	refErrs := mod.Validate()
	typeErrs := mod.TypeErrors()
	total := len(refErrs) + len(typeErrs) + len(crossErrs)

	if outputJSON(cmd) {
		refJSON := make([]jsonValidationError, 0, len(refErrs))
		crossJSON := make([]jsonValidationError, 0, len(crossErrs))
		typeJSON := make([]jsonTypeError, 0, len(typeErrs))
		// Refs and cross-errors both reuse ValidationError but have different
		// semantics. Keep them separate for consumers.
		for _, e := range refErrs {
			refJSON = append(refJSON, toJSONValErr(e))
		}
		for _, e := range crossErrs {
			crossJSON = append(crossJSON, toJSONValErr(e))
		}
		for _, e := range typeErrs {
			typeJSON = append(typeJSON, toJSONTypeErr(e))
		}
		code := 0
		if total > 0 {
			code = 1
		}
		exitJSON(struct {
			UndefinedReferences []jsonValidationError `json:"undefined_references"`
			CrossModuleIssues   []jsonValidationError `json:"cross_module_issues"`
			TypeErrors          []jsonTypeError       `json:"type_errors"`
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
// cross-module errors discovered by walking into locally-referenced child
// modules. For a single .tf file, cross-module checks are skipped (no tree).
func loadForValidate(path string) (*analysis.Module, []analysis.ValidationError) {
	info, err := os.Stat(path)
	if err != nil {
		fatalf("%v", err)
	}
	if !info.IsDir() {
		return mustLoadModule(path), nil
	}
	project, fileErrs, err := loader.LoadProject(path)
	if err != nil {
		fatalf("loading project: %v", err)
	}
	for _, fe := range fileErrs {
		fmt.Fprintf(os.Stderr, "warning: parse errors in %s\n", fe.Path)
		for _, e := range fe.Errors {
			fmt.Fprintf(os.Stderr, "  %s\n", e)
		}
	}
	return project.Root.Module, loader.CrossValidate(project)
}
