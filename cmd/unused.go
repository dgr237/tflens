package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/config"
	"github.com/dgr237/tflens/pkg/render"
)

var unusedCmd = &cobra.Command{
	Use:   "unused <path>",
	Short: "List entities that nothing in the module references",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runUnused(config.FromCommand(cmd, config.WithPath(args[0])))
	},
}

func init() {
	rootCmd.AddCommand(unusedCmd)
}

func runUnused(s config.Settings) {
	render.New(s).Unused(mustLoadModule(s).Unreferenced())
}
