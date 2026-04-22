package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var graphCmd = &cobra.Command{
	Use:   "graph <path>",
	Short: "Print the dependency graph in Graphviz DOT format",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runGraph(args[0])
	},
}

func init() {
	rootCmd.AddCommand(graphCmd)
}

func runGraph(path string) {
	mod := mustLoadModule(path)
	fmt.Print(mod.ToDOT())
}
