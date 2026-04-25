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
	if s.JSON {
		if cycles == nil {
			cycles = [][]string{}
		}
		exitJSON(struct {
			Cycles [][]string `json:"cycles"`
		}{Cycles: cycles}, exitCodeIfPositive(len(cycles)))
		return
	}
	render.WriteCycles(os.Stdout, cycles)
	if len(cycles) > 0 {
		os.Exit(1)
	}
}

// exitCodeIfPositive returns 1 when n > 0, else 0. Tiny helper used
// by subcommands whose JSON exit code mirrors a count.
func exitCodeIfPositive(n int) int {
	if n > 0 {
		return 1
	}
	return 0
}
