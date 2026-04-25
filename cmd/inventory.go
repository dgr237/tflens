package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/config"
	"github.com/dgr237/tflens/pkg/render"
)

var inventoryCmd = &cobra.Command{
	Use:   "inventory <path>",
	Short: "List all declared entities in a file or directory",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		s := config.FromCommand(cmd)
		s.Path = args[0]
		runInventory(s)
	},
}

func init() {
	rootCmd.AddCommand(inventoryCmd)
}

func runInventory(s config.Settings) {
	render.New(s.JSON, os.Stdout).Inventory(mustLoadModule(s.Path))
}
