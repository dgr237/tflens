package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/config"
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
		code := 0
		if len(cycles) > 0 {
			code = 1
		}
		exitJSON(struct {
			Cycles [][]string `json:"cycles"`
		}{Cycles: cycles}, code)
		return
	}
	if len(cycles) == 0 {
		fmt.Println("No cycles detected.")
		return
	}
	fmt.Printf("Cycles detected (%d):\n", len(cycles))
	for i, c := range cycles {
		fmt.Printf("  %d: %s\n", i+1, strings.Join(c, " → "))
	}
	os.Exit(1)
}
