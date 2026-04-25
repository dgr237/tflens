package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/config"
	"github.com/dgr237/tflens/pkg/render"
)

var depsCmd = &cobra.Command{
	Use:   "deps <path> <id>",
	Short: "Show direct dependencies and dependents of an entity",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		runDeps(config.FromCommand(cmd, config.WithPath(args[0])), args[1])
	},
}

func init() {
	rootCmd.AddCommand(depsCmd)
}

func runDeps(s config.Settings, id string) {
	mod := mustLoadModule(s)
	if !mod.HasEntity(id) {
		fatalf("entity %q not found in %s\nRun 'tflens inventory %s' to list available entities",
			id, s.Path, s.Path)
	}
	render.New(s).Deps(id, mod.Dependencies(id), mod.Dependents(id))
}
