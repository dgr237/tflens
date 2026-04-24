package cmd

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/render"
	"github.com/dgr237/tflens/pkg/resolver"
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
	newProj, err := loadProject(cmd, path)
	if err != nil {
		return fmt.Errorf("loading path: %w", err)
	}
	oldProj, cleanup, err := loadOldProjectForRef(cmd, path, baseRef)
	if err != nil {
		return err
	}
	defer cleanup()

	pairs := loader.PairModuleCalls(oldProj, newProj)
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].Key < pairs[j].Key })

	results := make([]refModuleResult, 0, len(pairs))
	totalBreaking := 0
	for _, p := range pairs {
		r := refModuleResult{Pair: p}
		if p.Status == loader.StatusChanged && p.OldNode != nil && p.NewNode != nil {
			// Local-source children are owned by this repo: their API is
			// implementation detail and only the parent's consumption
			// matters. External (registry/git) children come from a
			// publisher who's responsible for breaking-change discipline,
			// so we surface every API change.
			if resolver.IsLocalSource(p.NewSource) {
				r.Changes = diff.ConsumptionChangesForLocal(p)
			} else {
				r.Changes = diff.Diff(p.OldNode.Module, p.NewNode.Module)
			}
			// Tracked-attribute changes apply regardless of source type:
			// authors opt in to surface specific attributes (engine
			// versions, instance classes, …) the API diff intentionally
			// ignores. Pass the parent's call context so a marker in
			// the child catches changes flowing through the parent
			// (parent-side conditional, flipped variable default,
			// different local).
			r.Changes = append(r.Changes, diff.DiffTrackedCtx(p.OldNode.Module, p.NewNode.Module, diff.TrackedContext{
				OldParent: parentModule(p.OldParent),
				NewParent: parentModule(p.NewParent),
				CallName:  p.LocalName,
			})...)
			for _, c := range r.Changes {
				if c.Kind == diff.Breaking {
					totalBreaking++
				}
			}
		}
		results = append(results, r)
	}

	// The root module is not covered by loader.PairModuleCalls (which keys off
	// module CALLS). Diff it directly: a new required root variable, a
	// removed root output, a backend reconfiguration, etc. all matter to
	// the operator running `terraform plan` against this directory, even
	// though no parent module calls the root.
	oldRoot, newRoot := rootModule(oldProj), rootModule(newProj)
	rootChanges := diff.Diff(oldRoot, newRoot)
	rootChanges = append(rootChanges, diff.DiffTracked(oldRoot, newRoot)...)
	sort.Slice(rootChanges, func(i, j int) bool {
		if rootChanges[i].Kind != rootChanges[j].Kind {
			return rootChanges[i].Kind < rootChanges[j].Kind
		}
		return rootChanges[i].Subject < rootChanges[j].Subject
	})
	for _, c := range rootChanges {
		if c.Kind == diff.Breaking {
			totalBreaking++
		}
	}

	if outputJSON(cmd) {
		exitJSON(render.BuildJSONDiff(baseRef, path, results, rootChanges), diff.ExitCodeFor(totalBreaking))
		return nil
	}

	render.WriteDiffResults(os.Stdout, baseRef, results, rootChanges)
	if totalBreaking > 0 {
		os.Exit(1)
	}
	return nil
}

// rootModule returns p.Root.Module if both are non-nil, otherwise nil.
// diff.Diff and diff.DiffTracked are nil-safe so this just lets us avoid
// a nil chain.
func rootModule(p *loader.Project) *analysis.Module {
	if p == nil || p.Root == nil {
		return nil
	}
	return p.Root.Module
}

// parentModule returns n.Module if non-nil, else nil. The diff's
// TrackedContext is nil-safe so this just spares the call sites a nil
// check on the parent ModuleNode.
func parentModule(n *loader.ModuleNode) *analysis.Module {
	if n == nil {
		return nil
	}
	return n.Module
}

// refModuleResult is the per-module-call result type the cmd layer
// renders. It's a thin alias around diff.PairResult that lets the
// existing rendering / JSON code keep using r.Pair / r.Changes
// internally without reaching for the exported field names.
type refModuleResult = diff.PairResult


