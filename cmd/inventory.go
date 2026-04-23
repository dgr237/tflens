package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/hclbridge"
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
	var entities []analysis.Entity
	if os.Getenv("TFLENS_HCL2") == "1" {
		es, err := hclbridge.Load(path)
		if err != nil {
			fatalf("%v", err)
		}
		entities = es
	} else {
		mod := mustLoadModule(path)
		entities = mod.Entities()
	}

	if outputJSON(cmd) {
		out := make([]jsonEntity, 0, len(entities))
		for _, e := range entities {
			out = append(out, toJSONEntity(e))
		}
		emitJSON(struct {
			Total    int          `json:"total"`
			Entities []jsonEntity `json:"entities"`
		}{Total: len(out), Entities: out})
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
	fmt.Printf("Entities: %d\n", len(entities))
	for _, s := range sections {
		var filtered []analysis.Entity
		for _, e := range entities {
			if e.Kind == s.kind {
				filtered = append(filtered, e)
			}
		}
		if len(filtered) == 0 {
			continue
		}
		fmt.Printf("\n%s (%d):\n", s.title, len(filtered))
		for _, e := range filtered {
			if loc := e.Location(); loc != "" {
				fmt.Printf("  %-40s  (%s)\n", e.ID(), loc)
			} else {
				fmt.Printf("  %s\n", e.ID())
			}
		}
	}
}
