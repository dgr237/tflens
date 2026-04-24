package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var impactCmd = &cobra.Command{
	Use:   "impact <path> <id>",
	Short: "Show every entity transitively affected if <id> changes",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		runImpact(cmd, args[0], args[1])
	},
}

func init() {
	rootCmd.AddCommand(impactCmd)
}

func runImpact(cmd *cobra.Command, path, id string) {
	mod := mustLoadModule(path)
	if !mod.HasEntity(id) {
		fatalf("entity %q not found in %s\nRun 'tflens inventory %s' to list available entities",
			id, path, path)
	}

	affected := mod.Impact(id)
	if outputJSON(cmd) {
		if affected == nil {
			affected = []string{}
		}
		emitJSON(struct {
			Entity   string   `json:"entity"`
			Affected []string `json:"affected"`
		}{Entity: id, Affected: affected})
		return
	}
	if len(affected) == 0 {
		fmt.Printf("No entities are affected by changes to %s\n", id)
		return
	}

	fmt.Printf("If %s changes, %d %s affected (in evaluation order):\n",
		id, len(affected), plural(len(affected), "entity is", "entities are"))
	for _, aid := range affected {
		fmt.Printf("  %s\n", aid)
	}
}
