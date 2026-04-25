package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/config"
	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/render"
)

// tflensVersion is overwritten at release time via -ldflags. The export
// emits it under top-level "tflens_version" so consumers can correlate
// shape changes against a specific build.
var tflensVersion = "dev"

var exportCmd = &cobra.Command{
	Use:   "export [path]",
	Short: "[EXPERIMENTAL] Emit the enriched module model as JSON",
	Long: `[EXPERIMENTAL — output shape subject to change]

Walks the project rooted at <path> (default: cwd) and emits the
enriched entity model that tflens has built up internally — entity
declarations with type info, evaluated values where statically
resolvable via the curated stdlib, the dependency graph, and any
` + "`# tflens:track`" + ` markers — as a single JSON document on stdout.

Intended as a building block for downstream converters that want to
translate Terraform configurations into other provisioning systems
(kro, crossplane, Pulumi, etc.) without re-implementing the parsing /
type inference / cross-module resolution layers.

Output is always JSON regardless of --format. The shape is versioned
under "schema_version" and explicitly flagged "_experimental": true.
Do not depend on field stability across minor versions until the
prototype graduates.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		s := config.FromCommand(cmd, config.WithPath(pathArg(args, 0)))
		return runExport(s)
	},
}

func init() {
	rootCmd.AddCommand(exportCmd)
}

func runExport(s config.Settings) error {
	p, fileErrs, err := loader.New(s).Project(s.Path)
	if err != nil {
		return err
	}
	printFileErrs(s, fileErrs)
	exp := render.BuildExport(p, tflensVersion)
	return render.WriteExport(exp, s.Out)
}
