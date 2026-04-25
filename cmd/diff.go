package cmd

import (
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/config"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/plan"
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
		s := config.FromCommand(cmd, config.WithPath(pathArg(args, 0)))
		if err := resolveAutoBaseRef(&s); err != nil {
			return err
		}
		return runDiffRef(s)
	},
}

func init() {
	diffCmd.Flags().String("ref", config.RefAutoKeyword,
		"git ref to compare against (branch, tag, SHA, …); 'auto' detects @{upstream} → origin/HEAD → main → master")
	diffCmd.Flags().String("enrich-with-plan", "",
		"path to a `terraform show -json` plan file. Plan-derived attribute deltas (cidr_block changes, force-new attributes, etc.) get folded into the diff result alongside static-analysis findings — bridges the gap that otherwise leaves resource-attribute changes invisible to tflens. Force-new attributes become Breaking; other attribute changes become Informational. Plan-derived rows get a 📋 badge in the markdown renderer.")
	rootCmd.AddCommand(diffCmd)
}

func runDiffRef(s config.Settings) error {
	oldProj, newProj, cleanup, err := loader.New(s).ProjectsForDiff(s.Path, s.BaseRef)
	if err != nil {
		return err
	}
	defer cleanup()
	results, rootChanges, breaking := diff.AnalyzeProjects(oldProj, newProj)
	if s.PlanPath != "" {
		p, err := plan.Load(s.PlanPath)
		if err != nil {
			cleanup()
			return err
		}
		// First-cut routing: every plan-derived change attaches to
		// rootChanges with the full plan address as Subject. A
		// future iteration would route module.X.* entries into the
		// matching PairResult.Changes for tighter per-module
		// presentation. For now the renderers handle the flat list
		// fine via the Source field tagging.
		rootChanges = diff.EnrichFromPlan(rootChanges, p, newProj)
		// Recompute the breaking count to include plan-derived
		// findings — otherwise the CI exit code wouldn't fire on
		// plan-only Breaking changes (e.g. a force-new attribute
		// change that the source diff couldn't see).
		breaking = countBreaking(rootChanges, results)
	}
	render.New(s).Diff(s.BaseRef, s.Path, results, rootChanges)
	if breaking > 0 {
		// os.Exit skips the deferred cleanup, so run it explicitly
		// to avoid leaking the temporary git worktree on every
		// CI-gating run that finds breaking changes.
		cleanup()
		os.Exit(1)
	}
	return nil
}

// countBreaking sums the Breaking changes across rootChanges + every
// PairResult. Used after plan enrichment to refresh the CI-gating
// count — diff.AnalyzeProjects's original count predates the plan-
// derived additions.
func countBreaking(rootChanges []diff.Change, results []diff.PairResult) int {
	n := 0
	for _, c := range rootChanges {
		if c.Kind == diff.Breaking {
			n++
		}
	}
	for _, r := range results {
		for _, c := range r.Changes {
			if c.Kind == diff.Breaking {
				n++
			}
		}
	}
	return n
}
