package cmd

import (
	"os"

	"github.com/spf13/cobra"

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
		runValidate(config.FromCommand(cmd, config.WithPath(args[0])))
	},
}

func init() {
	rootCmd.AddCommand(validateCmd)
}

func runValidate(s config.Settings) {
	mod, crossErrs, fileErrs, err := loader.New(s).ForValidate(s.Path)
	if err != nil {
		fatalf("%v", err)
	}
	printFileErrs(s, fileErrs)
	refErrs := mod.Validate()
	typeErrs := mod.TypeErrors()
	total := len(refErrs) + len(typeErrs) + len(crossErrs)
	// Errors go to stderr so they don't pollute pipes; the JSON and
	// markdown outputs stay on stdout (single pipeable stream).
	// Mutate the local Settings copy so render.New picks up the right
	// writer.
	if total > 0 && !s.JSON && !s.Markdown {
		s.Out = s.Err
	}
	render.New(s).Validate(refErrs, crossErrs, typeErrs)
	if total > 0 {
		os.Exit(1)
	}
}
