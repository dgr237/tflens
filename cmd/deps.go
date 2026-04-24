package cmd

import (
	"fmt"

	"github.com/spf13/cobra"
)

var depsCmd = &cobra.Command{
	Use:   "deps <path> <id>",
	Short: "Show direct dependencies and dependents of an entity",
	Args:  cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		runDeps(cmd, args[0], args[1])
	},
}

func init() {
	rootCmd.AddCommand(depsCmd)
}

func runDeps(cmd *cobra.Command, path, id string) {
	mod := mustLoadModule(path)
	if !mod.HasEntity(id) {
		fatalf("entity %q not found in %s\nRun 'tflens inventory %s' to list available entities",
			id, path, path)
	}

	deps := mod.Dependencies(id)
	dependents := mod.Dependents(id)

	if outputJSON(cmd) {
		emitJSON(struct {
			Entity       string   `json:"entity"`
			DependsOn    []string `json:"depends_on"`
			ReferencedBy []string `json:"referenced_by"`
		}{Entity: id, DependsOn: deps, ReferencedBy: dependents})
		return
	}

	fmt.Printf("Entity:  %s\n", id)

	fmt.Printf("\nDepends on (%d):\n", len(deps))
	if len(deps) == 0 {
		fmt.Println("  (none)")
	}
	for _, d := range deps {
		fmt.Printf("  %s\n", d)
	}

	fmt.Printf("\nReferenced by (%d):\n", len(dependents))
	if len(dependents) == 0 {
		fmt.Println("  (none)")
	}
	for _, d := range dependents {
		fmt.Printf("  %s\n", d)
	}
}
