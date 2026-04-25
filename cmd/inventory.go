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
	mod := mustLoadModule(s.Path)
	if s.JSON {
		entities := make([]render.JSONEntity, 0, len(mod.Entities()))
		for _, e := range mod.Entities() {
			entities = append(entities, render.JSONEnt(e))
		}
		emitJSON(struct {
			Total    int                 `json:"total"`
			Entities []render.JSONEntity `json:"entities"`
		}{Total: len(entities), Entities: entities})
		return
	}
	render.WriteInventory(os.Stdout, mod)
}
