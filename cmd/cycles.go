package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/config"
	"github.com/dgr237/tflens/pkg/render"
)

var cyclesCmd = &cobra.Command{
	Use:   "cycles <path>",
	Short: "Detect and print dependency cycles (exits non-zero if any found)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runCycles(config.FromCommand(cmd, config.WithPath(args[0])))
	},
}

func init() {
	rootCmd.AddCommand(cyclesCmd)
}

func runCycles(s config.Settings) {
	mod := mustLoadModule(s)
	cycles := mod.Cycles()
	render.New(s).Cycles(cycles)
	if len(cycles) > 0 {
		os.Exit(1)
	}
}
