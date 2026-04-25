package cmd

import (
	"io"
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/config"
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
		s := config.FromCommand(cmd)
		s.Path = args[0]
		runValidate(s)
	},
}

func init() {
	rootCmd.AddCommand(validateCmd)
}

func runValidate(s config.Settings) {
	mod, crossErrs, fileErrs, err := loader.LoadForValidate(s.Path, s.Offline)
	if err != nil {
		fatalf("%v", err)
	}
	printFileErrs(fileErrs)
	refErrs := mod.Validate()
	typeErrs := mod.TypeErrors()
	total := len(refErrs) + len(typeErrs) + len(crossErrs)
	if s.JSON {
		emitValidateJSON(refErrs, crossErrs, typeErrs, total)
		return
	}
	// Errors go to stderr so they don't pollute pipes; the success
	// message goes to stdout.
	var w io.Writer = os.Stdout
	if total > 0 {
		w = os.Stderr
	}
	render.WriteValidate(w, refErrs, crossErrs, typeErrs)
	if total > 0 {
		os.Exit(1)
	}
}

// emitValidateJSON builds the structured envelope for `validate
// --format=json`. Refs and cross-errors share the ValidationError
// type but mean different things; the wire format keeps them in
// distinct top-level keys for consumers.
func emitValidateJSON(
	refErrs, crossErrs []analysis.ValidationError,
	typeErrs []analysis.TypeCheckError,
	total int,
) {
	refJSON := make([]render.JSONValidationError, 0, len(refErrs))
	for _, e := range refErrs {
		refJSON = append(refJSON, render.JSONValErr(e))
	}
	crossJSON := make([]render.JSONValidationError, 0, len(crossErrs))
	for _, e := range crossErrs {
		crossJSON = append(crossJSON, render.JSONValErr(e))
	}
	typeJSON := make([]render.JSONTypeError, 0, len(typeErrs))
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
}
