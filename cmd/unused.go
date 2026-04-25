package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/config"
	"github.com/dgr237/tflens/pkg/render"
)

var unusedCmd = &cobra.Command{
	Use:   "unused <path>",
	Short: "List entities that nothing in the module references",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		s := config.FromCommand(cmd)
		s.Path = args[0]
		runUnused(s)
	},
}

func init() {
	rootCmd.AddCommand(unusedCmd)
}

func runUnused(s config.Settings) {
	mod := mustLoadModule(s.Path)
	render.New(s.JSON, os.Stdout).Unused(mod.Unreferenced())
}
