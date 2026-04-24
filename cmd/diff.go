package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/render"
)

var diffCmd = &cobra.Command{
	Use:   "diff [path]",
	Short: "Compare module APIs in path against a git ref (author view)",
	Long: `Diff is the author view: it answers "what changed in the module's
API between this checkout and the base ref?". Use it when reviewing a
module release or PR; pair it with whatif (consumer view) when you want
to know whether your specific caller breaks.

Compares every module call in path (default cwd) against its counterpart
at the given git ref (branch, tag, SHA, origin/main, HEAD~3, …).
Classifies each detected change as:
  - Breaking: existing callers or state will be affected
  - NonBreaking: safe to upgrade through
  - Informational: operational or cosmetic, but worth surfacing

Exits non-zero when any Breaking changes exist (suitable for CI gating).

The ref defaults to 'auto', which resolves to @{upstream} → origin/HEAD
→ main → master.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "."
		if len(args) == 1 {
			path = args[0]
		}
		base, _ := cmd.Flags().GetString("ref")
		if base == RefAutoKeyword {
			auto, err := resolveAutoRef(path)
			if err != nil {
				return err
			}
			base = auto
		}
		return runDiffRef(cmd, path, base)
	},
}

func init() {
	diffCmd.Flags().String("ref", RefAutoKeyword,
		"git ref to compare against (branch, tag, SHA, …); 'auto' detects @{upstream} → origin/HEAD → main → master")
	rootCmd.AddCommand(diffCmd)
}

func runDiffRef(cmd *cobra.Command, path, baseRef string) error {
	oldProj, newProj, cleanup, err := loadOldAndNew(cmd, path, baseRef)
	if err != nil {
		return err
	}
	defer cleanup()
	results, rootChanges, breaking := diff.AnalyzeProjects(oldProj, newProj)
	if outputJSON(cmd) {
		exitJSON(render.BuildJSONDiff(baseRef, path, results, rootChanges), diff.ExitCodeFor(breaking))
		return nil
	}
	render.WriteDiffResults(os.Stdout, baseRef, results, rootChanges)
	if breaking > 0 {
		os.Exit(1)
	}
	return nil
}


