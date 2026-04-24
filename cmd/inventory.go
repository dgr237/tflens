package cmd

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/render"
	"github.com/dgr237/tflens/pkg/analysis"
)

var inventoryCmd = &cobra.Command{
	Use:   "inventory <path>",
	Short: "List all declared entities in a file or directory",
	Args:  cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runInventory(cmd, args[0])
	},
}

func init() {
	rootCmd.AddCommand(inventoryCmd)
}

func runInventory(cmd *cobra.Command, path string) {
	mod := mustLoadModule(path)
	if outputJSON(cmd) {
		entities := make([]render.JSONEntity, 0, len(mod.Entities()))
		for _, e := range mod.Entities() {
			entities = append(entities, render.JSONEnt(e))
		}
		emitJSON(struct {
			Total    int          `json:"total"`
			Entities []render.JSONEntity `json:"entities"`
		}{Total: len(entities), Entities: entities})
		return
	}
	sections := []struct {
		kind  analysis.EntityKind
		title string
	}{
		{analysis.KindVariable, "Variables"},
		{analysis.KindLocal, "Locals"},
		{analysis.KindData, "Data sources"},
		{analysis.KindResource, "Resources"},
		{analysis.KindModule, "Modules"},
		{analysis.KindOutput, "Outputs"},
	}
	total := len(mod.Entities())
	fmt.Printf("Entities: %d\n", total)
	for _, s := range sections {
		entities := mod.Filter(s.kind)
		if len(entities) == 0 {
			continue
		}
		fmt.Printf("\n%s (%d):\n", s.title, len(entities))
		for _, e := range entities {
			if loc := e.Location(); loc != "" {
				fmt.Printf("  %-40s  (%s)\n", e.ID(), loc)
			} else {
				fmt.Printf("  %s\n", e.ID())
			}
		}
	}
}
