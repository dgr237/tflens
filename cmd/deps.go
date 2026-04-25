package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/config"
	"github.com/dgr237/tflens/pkg/render"
)

var depsCmd = &cobra.Command{
	Use:   "deps <path> <id>",
	Short: "Show direct dependencies and dependents of an entity",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		s := config.FromCommand(cmd)
		s.Path = args[0]
		runDeps(s, args[1])
	},
}

func init() {
	rootCmd.AddCommand(depsCmd)
}

func runDeps(s config.Settings, id string) {
	mod := mustLoadModule(s.Path)
	if !mod.HasEntity(id) {
		fatalf("entity %q not found in %s\nRun 'tflens inventory %s' to list available entities",
			id, s.Path, s.Path)
	}
	render.New(s.JSON, os.Stdout).Deps(id, mod.Dependencies(id), mod.Dependents(id))
}
