package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/diff"
)

var diffCmd = &cobra.Command{
	Use:   "diff <old> <new>",
	Short: "Compare two module versions and report breaking changes",
	Long: `Diff classifies every detected change as:
  - Breaking: existing callers or state will be affected
  - NonBreaking: safe to upgrade through
  - Informational: operational or cosmetic, but worth surfacing

Exits non-zero when any Breaking changes exist (suitable for CI gating).`,
	Args: cobra.ExactArgs(2),
	Run: func(cmd *cobra.Command, args []string) {
		runDiff(cmd, args[0], args[1])
	},
}

func init() {
	rootCmd.AddCommand(diffCmd)
}

func runDiff(cmd *cobra.Command, oldPath, newPath string) {
	oldMod := mustLoadModule(oldPath)
	newMod := mustLoadModule(newPath)
	changes := diff.Diff(oldMod, newMod)

	var breaking, nonBreaking, info []diff.Change
	for _, c := range changes {
		switch c.Kind {
		case diff.Breaking:
			breaking = append(breaking, c)
		case diff.NonBreaking:
			nonBreaking = append(nonBreaking, c)
		case diff.Informational:
			info = append(info, c)
		}
	}

	if outputJSON(cmd) {
		all := make([]jsonChange, 0, len(changes))
		for _, c := range changes {
			all = append(all, toJSONChange(c))
		}
		code := 0
		if len(breaking) > 0 {
			code = 1
		}
		exitJSON(struct {
			Changes []jsonChange `json:"changes"`
			Summary struct {
				Breaking      int `json:"breaking"`
				NonBreaking   int `json:"non_breaking"`
				Informational int `json:"informational"`
			} `json:"summary"`
		}{
			Changes: all,
			Summary: struct {
				Breaking      int `json:"breaking"`
				NonBreaking   int `json:"non_breaking"`
				Informational int `json:"informational"`
			}{len(breaking), len(nonBreaking), len(info)},
		}, code)
		return
	}

	if len(breaking)+len(nonBreaking)+len(info) == 0 {
		fmt.Println("No changes detected.")
		return
	}

	printSection := func(title string, list []diff.Change) {
		if len(list) == 0 {
			return
		}
		fmt.Printf("%s (%d):\n", title, len(list))
		for _, c := range list {
			fmt.Printf("  %s: %s\n", c.Subject, c.Detail)
		}
	}
	printSection("Breaking changes", breaking)
	if len(breaking) > 0 && len(nonBreaking)+len(info) > 0 {
		fmt.Println()
	}
	printSection("Non-breaking changes", nonBreaking)
	if len(nonBreaking) > 0 && len(info) > 0 {
		fmt.Println()
	}
	printSection("Informational", info)

	if len(breaking) > 0 {
		os.Exit(1)
	}
}
