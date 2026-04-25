package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/config"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
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
		s := config.FromCommand(cmd)
		s.Path = pathArg(args, 0)
		if err := resolveAutoBaseRef(&s); err != nil {
			return err
		}
		return runDiffRef(s)
	},
}

func init() {
	diffCmd.Flags().String("ref", config.RefAutoKeyword,
		"git ref to compare against (branch, tag, SHA, …); 'auto' detects @{upstream} → origin/HEAD → main → master")
	rootCmd.AddCommand(diffCmd)
}

func runDiffRef(s config.Settings) error {
	oldProj, newProj, cleanup, err := loader.LoadProjectsForDiff(s.Path, s.BaseRef, s.Offline)
	if err != nil {
		return err
	}
	defer cleanup()
	results, rootChanges, breaking := diff.AnalyzeProjects(oldProj, newProj)
	render.New(s.JSON, os.Stdout).Diff(s.BaseRef, s.Path, results, rootChanges)
	if breaking > 0 {
		os.Exit(1)
	}
	return nil
}
