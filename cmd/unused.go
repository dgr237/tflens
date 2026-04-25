package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/config"
	"github.com/dgr237/tflens/pkg/render"
)

var unusedCmd = &cobra.Command{
	Use:   "unused <path>",
	Short: "List entities that nothing in the module references",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		s := config.FromCommand(cmd)
		s.Path = args[0]
		runUnused(s)
	},
}

func init() {
	rootCmd.AddCommand(unusedCmd)
}

func runUnused(s config.Settings) {
	mod := mustLoadModule(s.Path)
	unused := mod.Unreferenced()
	if s.JSON {
		entities := make([]render.JSONEntity, 0, len(unused))
		for _, e := range unused {
			entities = append(entities, render.JSONEnt(e))
		}
		emitJSON(struct {
			Unreferenced []render.JSONEntity `json:"unreferenced"`
		}{Unreferenced: entities})
		return
	}
	if len(unused) == 0 {
		fmt.Println("No unreferenced entities found.")
		return
	}
	fmt.Printf("Unreferenced entities (%d):\n", len(unused))
	for _, e := range unused {
		fmt.Printf("  %s\n", e.ID())
	}
}
