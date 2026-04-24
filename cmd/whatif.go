package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/render"
)

var whatifCmd = &cobra.Command{
	Use:   "whatif [path] [module-call-name]",
	Short: "Simulate module-call upgrades and report whether the caller breaks (consumer view)",
	Long: `Whatif is the consumer view: it answers "if I merged the working
tree's module changes, would my parent still work?". Strictly more
focused than diff — a child can ship many Breaking API changes that
don't affect a particular caller, and whatif suppresses those by
cross-validating the parent's argument set against the candidate
child's variables.

For every module call in path (default cwd) that differs between
the working tree and the base ref, whatif loads the parent at base,
loads the candidate child from the working tree, and reports:

  1. Direct impact on the parent — missing required inputs, unknown
     arguments, type mismatches the upgrade would introduce.
  2. Full API diff between the base and working-tree child, for context.

With an optional module-call-name, scope to a single call. Exits
non-zero when the direct-impact list is non-empty.

The ref defaults to 'auto', which resolves to @{upstream} → origin/HEAD
→ main → master.`,
	Args: cobra.MaximumNArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		path := "."
		name := ""
		if len(args) >= 1 {
			path = args[0]
		}
		if len(args) == 2 {
			name = args[1]
		}
		base, _ := cmd.Flags().GetString("ref")
		if base == RefAutoKeyword {
			auto, err := resolveAutoRef(path)
			if err != nil {
				return err
			}
			base = auto
		}
		return runWhatifRef(cmd, path, base, name)
	},
}

func init() {
	whatifCmd.Flags().String("ref", RefAutoKeyword,
		"git ref to compare against (branch, tag, SHA, …); 'auto' detects @{upstream} → origin/HEAD → main → master")
	rootCmd.AddCommand(whatifCmd)
}

// runWhatifRef simulates merging the working tree's module upgrades
// against callers at baseRef. If only is non-empty it restricts to that
// one call name; otherwise every call that differs is simulated.
func runWhatifRef(cmd *cobra.Command, path, baseRef, only string) error {
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
	if only != "" {
		filtered := pairs[:0]
		for _, p := range pairs {
			if p.Key == only || p.LocalName == only {
				filtered = append(filtered, p)
			}
		}
		pairs = filtered
		if len(pairs) == 0 {
			return fmt.Errorf("no module call named %q differs between %s and the path (or call does not exist)", only, baseRef)
		}
	}

	var calls []whatifCallResult
	totalImpact := 0
	for _, p := range pairs {
		// whatif is only meaningful for calls that existed at base — we
		// need an "old parent" that called an "old child" to diff
		// against the new child. Added calls have no base-side caller.
		if p.Status == loader.StatusAdded {
			continue
		}
		r := diff.BuildWhatifResult(p)
		totalImpact += len(r.DirectImpact)
		calls = append(calls, r)
	}

	if outputJSON(cmd) {
		exitJSON(render.BuildJSONWhatif(baseRef, path, calls), diff.ExitCodeFor(totalImpact))
		return nil
	}

	printWhatifBranchResults(baseRef, path, calls)
	if totalImpact > 0 {
		os.Exit(1)
	}
	return nil
}

// whatifCallResult is a thin alias around diff.WhatifResult so the
// rendering / JSON code below keeps a stable local name. The fields
// (Pair, DirectImpact, APIChanges) are exported on the underlying
// type — the rendering code below references them directly.
type whatifCallResult = diff.WhatifResult

// ---- text rendering ----

func printWhatifBranchResults(baseRef, path string, calls []whatifCallResult) {
	if len(calls) == 0 {
		fmt.Printf("No upgraded module calls to simulate (path vs %s).\n", baseRef)
		return
	}
	for i, r := range calls {
		if i > 0 {
			fmt.Println()
		}
		printOneWhatifCall(path, r)
	}
}

func printOneWhatifCall(path string, r whatifCallResult) {
	if r.Pair.Status == loader.StatusRemoved {
		fmt.Printf("module.%s: REMOVED (was source=%s, version=%q)\n",
			r.Pair.Key, r.Pair.OldSource, r.Pair.OldVersion)
		return
	}
	fmt.Printf("Direct impact on module.%s in %s (%d issue(s)):\n",
		r.Pair.Key, path, len(r.DirectImpact))
	if len(r.DirectImpact) == 0 {
		fmt.Println("  (none — callers at base are compatible with the new child)")
	} else {
		for _, e := range r.DirectImpact {
			fmt.Printf("  %s\n", e)
		}
	}
	if len(r.APIChanges) == 0 {
		return
	}
	fmt.Println()
	fmt.Printf("  API changes for module.%s:\n", r.Pair.Key)
	render.WriteChangesByKind(os.Stdout, "    ", "      ", r.APIChanges)
}

