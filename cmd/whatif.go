package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
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
		r := buildWhatifCallResult(p)
		totalImpact += len(r.directImpact)
		calls = append(calls, r)
	}

	if outputJSON(cmd) {
		exitJSON(whatifBranchJSONPayload(baseRef, path, calls), diff.ExitCodeFor(totalImpact))
		return nil
	}

	printWhatifBranchResults(baseRef, path, calls)
	if totalImpact > 0 {
		os.Exit(1)
	}
	return nil
}

type whatifCallResult struct {
	pair         loader.ModuleCallPair
	directImpact []analysis.ValidationError
	apiChanges   []diff.Change // empty when we cannot compute (removed or missing child)
}

func buildWhatifCallResult(p loader.ModuleCallPair) whatifCallResult {
	r := whatifCallResult{pair: p}
	// Direct impact: does the old parent's usage break under the new
	// child's API? For "removed" calls there's no new child, so we can
	// only note the removal.
	if p.Status == loader.StatusRemoved {
		return r
	}
	if p.NewNode == nil || p.OldParent == nil {
		// No child API available OR the old side doesn't have a
		// parent to cross-validate against (e.g. nested call's parent
		// was itself added in the new tree).
		if p.OldNode != nil && p.NewNode != nil {
			r.apiChanges = diff.Diff(p.OldNode.Module, p.NewNode.Module)
		}
		return r
	}
	r.directImpact = loader.CrossValidateCall(p.OldParent.Module, p.LocalName, p.NewNode.Module)
	if p.OldNode != nil {
		r.apiChanges = diff.Diff(p.OldNode.Module, p.NewNode.Module)
	}
	return r
}

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
	if r.pair.Status == loader.StatusRemoved {
		fmt.Printf("module.%s: REMOVED (was source=%s, version=%q)\n",
			r.pair.Key, r.pair.OldSource, r.pair.OldVersion)
		return
	}
	fmt.Printf("Direct impact on module.%s in %s (%d issue(s)):\n",
		r.pair.Key, path, len(r.directImpact))
	if len(r.directImpact) == 0 {
		fmt.Println("  (none — callers at base are compatible with the new child)")
	} else {
		for _, e := range r.directImpact {
			fmt.Printf("  %s\n", e)
		}
	}
	if len(r.apiChanges) == 0 {
		return
	}
	var breaking, nonBreaking, info []diff.Change
	for _, c := range r.apiChanges {
		switch c.Kind {
		case diff.Breaking:
			breaking = append(breaking, c)
		case diff.NonBreaking:
			nonBreaking = append(nonBreaking, c)
		case diff.Informational:
			info = append(info, c)
		}
	}
	fmt.Println()
	fmt.Printf("  API changes for module.%s:\n", r.pair.Key)
	section := func(title string, list []diff.Change) {
		if len(list) == 0 {
			return
		}
		fmt.Printf("    %s (%d):\n", title, len(list))
		for _, c := range list {
			printChangeLine("      ", c)
		}
	}
	section("Breaking", breaking)
	section("Non-breaking", nonBreaking)
	section("Informational", info)
}

// ---- JSON rendering ----

type whatifBranchJSON struct {
	BaseRef   string                 `json:"base_ref"`
	Path string                 `json:"path"`
	Calls     []whatifCallJSON       `json:"calls"`
	Summary   whatifBranchSummaryJSON `json:"summary"`
}

type whatifCallJSON struct {
	Name         string                `json:"name"`
	Status       string                `json:"status"`
	DirectImpact []jsonValidationError `json:"direct_impact"`
	APIChanges   []jsonChange          `json:"api_changes,omitempty"`
}

type whatifBranchSummaryJSON struct {
	DirectImpact int `json:"direct_impact"`
	Breaking     int `json:"breaking"`
	NonBreaking  int `json:"non_breaking"`
	Informational int `json:"informational"`
}

func whatifBranchJSONPayload(baseRef, path string, calls []whatifCallResult) whatifBranchJSON {
	out := whatifBranchJSON{BaseRef: baseRef, Path: path}
	for _, r := range calls {
		entry := whatifCallJSON{
			Name:   r.pair.Key,
			Status: r.pair.Status.String(),
		}
		for _, e := range r.directImpact {
			entry.DirectImpact = append(entry.DirectImpact, toJSONValErr(e))
			out.Summary.DirectImpact++
		}
		for _, c := range r.apiChanges {
			entry.APIChanges = append(entry.APIChanges, toJSONChange(c))
			switch c.Kind {
			case diff.Breaking:
				out.Summary.Breaking++
			case diff.NonBreaking:
				out.Summary.NonBreaking++
			case diff.Informational:
				out.Summary.Informational++
			}
		}
		out.Calls = append(out.Calls, entry)
	}
	return out
}
