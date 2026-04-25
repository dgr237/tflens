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
		s := config.FromCommand(cmd)
		s.Path = args[0]
		runCycles(s)
	},
}

func init() {
	rootCmd.AddCommand(cyclesCmd)
}

func runCycles(s config.Settings) {
	mod := mustLoadModule(s.Path)
	cycles := mod.Cycles()
	render.New(s.JSON, os.Stdout).Cycles(cycles)
	if len(cycles) > 0 {
		os.Exit(1)
	}
}
