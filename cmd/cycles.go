package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"
)

var cyclesCmd = &cobra.Command{
	Use:   "cycles <path>",
	Short: "Detect and print dependency cycles (exits non-zero if any found)",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runCycles(cmd, args[0])
	},
}

func init() {
	rootCmd.AddCommand(cyclesCmd)
}

func runCycles(cmd *cobra.Command, path string) {
	mod := mustLoadModule(path)
	cycles := mod.Cycles()
	if outputJSON(cmd) {
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
