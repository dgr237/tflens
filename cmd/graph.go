package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/config"
)

var graphCmd = &cobra.Command{
	Use:   "graph <path>",
	Short: "Print the dependency graph in Graphviz DOT format",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runGraph(config.FromCommand(cmd, config.WithPath(args[0])))
	},
}

func init() {
	rootCmd.AddCommand(graphCmd)
}

func runGraph(s config.Settings) {
	fmt.Fprint(s.Out, mustLoadModule(s).ToDOT())
}
