package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/render"
)

var unusedCmd = &cobra.Command{
	Use:   "unused <path>",
	Short: "List entities that nothing in the module references",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runUnused(cmd, args[0])
	},
}

func init() {
	rootCmd.AddCommand(unusedCmd)
}

func runUnused(cmd *cobra.Command, path string) {
	mod := mustLoadModule(path)
	unused := mod.Unreferenced()
	if outputJSON(cmd) {
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
