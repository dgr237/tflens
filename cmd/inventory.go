package cmd

import (
	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/config"
	"github.com/dgr237/tflens/pkg/render"
)

var inventoryCmd = &cobra.Command{
	Use:   "inventory <path>",
	Short: "List all declared entities in a file or directory",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runInventory(config.FromCommand(cmd, config.WithPath(args[0])))
	},
}

func init() {
	rootCmd.AddCommand(inventoryCmd)
}

func runInventory(s config.Settings) {
	render.New(s).Inventory(mustLoadModule(s))
}
