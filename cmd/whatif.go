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
	Use:   "whatif <workspace> <module-call-name> <new-version-path>",
	Short: "Simulate upgrading a module call to a candidate new version",
	Long: `Answers: if I upgraded module.<name> to this candidate version, what
would break in my current workspace?

Loads the parent workspace (using .terraform/modules/modules.json when
present to find the currently-installed child), loads the candidate new
version as a standalone module, and reports:

  1. Direct impact on the parent — missing required inputs, unknown
     arguments, type mismatches the upgrade would introduce.
  2. Full API diff between the currently-installed child and the candidate,
     for context.

Exits non-zero when the parent would break.

Two modes:

  tflens whatif <workspace> <name> <new-version-path>
      Explicit: parent is <workspace>, candidate child is at <path>.

  tflens whatif --branch <base> [workspace] [name]
      Branch: parent is the workspace checked out at git ref <base>;
      candidate child is whatever the working tree resolves to now.
      With no <name>, every module call that differs between base and
      working tree is simulated.`,
	Args: cobra.RangeArgs(0, 3),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, _ := cmd.Flags().GetString("branch")
		if base != "" {
			if len(args) > 2 {
				return fmt.Errorf("--branch mode takes at most 2 positional args (workspace, name); got %d", len(args))
			}
			ws := "."
			name := ""
			if len(args) >= 1 {
				ws = args[0]
			}
			if len(args) == 2 {
				name = args[1]
			}
			if base == BranchAutoKeyword {
				auto, err := resolveAutoBase(ws)
				if err != nil {
					return err
				}
				base = auto
			}
			return runWhatifBranch(cmd, ws, base, name)
		}
		if len(args) != 3 {
			return fmt.Errorf("whatif requires <workspace> <name> <new-version-path>, or --branch <base> [workspace] [name]")
		}
		runWhatif(cmd, args[0], args[1], args[2])
		return nil
	},
}

func init() {
	whatifCmd.Flags().String("branch", "",
		"simulate upgrades derived from the working-tree vs git ref <base>; pass 'auto' to detect")
	rootCmd.AddCommand(whatifCmd)
}

func runWhatif(cmd *cobra.Command, workspace, moduleCallName, newVersionPath string) {
	info, err := os.Stat(workspace)
	if err != nil {
		fatalf("%v", err)
	}
	if !info.IsDir() {
		fatalf("whatif requires a workspace directory, got a file")
	}
	project, err := loadProject(cmd, workspace)
	if err != nil {
		fatalf("loading workspace: %v", err)
	}
	parent := project.Root.Module

	// Confirm the module call exists in the parent.
	var found bool
	for _, e := range parent.Filter(analysis.KindModule) {
		if e.Name == moduleCallName {
			found = true
			break
		}
	}
	if !found {
		fatalf("no module call named %q in %s", moduleCallName, workspace)
	}

	// Find the currently-installed child (for the API diff).
	oldChild, oldChildFound := project.Root.Children[moduleCallName]
	if !oldChildFound {
		fmt.Fprintf(os.Stderr,
			"note: current version of module %q is not on disk (no manifest or local source); running parent-vs-new checks only\n",
			moduleCallName)
	}

	// Load the candidate new version.
	newChild, newFileErrs, err := loader.LoadDir(newVersionPath)
	if err != nil {
		fatalf("loading new version from %s: %v", newVersionPath, err)
	}
	for _, fe := range newFileErrs {
		fmt.Fprintf(os.Stderr, "warning: parse errors in candidate %s\n", fe.Path)
		for _, e := range fe.Errors {
			fmt.Fprintf(os.Stderr, "  %s\n", e)
		}
	}

	// (1) Direct impact on the parent.
	impact := loader.CrossValidateCall(parent, moduleCallName, newChild)

	if outputJSON(cmd) {
		impactJSON := make([]jsonValidationError, 0, len(impact))
		for _, e := range impact {
			impactJSON = append(impactJSON, toJSONValErr(e))
		}
		out := struct {
			ModuleCallName string                `json:"module_call_name"`
			Workspace      string                `json:"workspace"`
			NewVersionPath string                `json:"new_version_path"`
			DirectImpact   []jsonValidationError `json:"direct_impact"`
			APIChanges     *struct {
				Changes []jsonChange `json:"changes"`
				Summary struct {
					Breaking      int `json:"breaking"`
					NonBreaking   int `json:"non_breaking"`
					Informational int `json:"informational"`
				} `json:"summary"`
			} `json:"api_changes,omitempty"`
		}{
			ModuleCallName: moduleCallName,
			Workspace:      workspace,
			NewVersionPath: newVersionPath,
			DirectImpact:   impactJSON,
		}
		if oldChildFound {
			changes := diff.Diff(oldChild.Module, newChild)
			var brk, nb, info int
			allJSON := make([]jsonChange, 0, len(changes))
			for _, c := range changes {
				allJSON = append(allJSON, toJSONChange(c))
				switch c.Kind {
				case diff.Breaking:
					brk++
				case diff.NonBreaking:
					nb++
				case diff.Informational:
					info++
				}
			}
			out.APIChanges = &struct {
				Changes []jsonChange `json:"changes"`
				Summary struct {
					Breaking      int `json:"breaking"`
					NonBreaking   int `json:"non_breaking"`
					Informational int `json:"informational"`
				} `json:"summary"`
			}{
				Changes: allJSON,
				Summary: struct {
					Breaking      int `json:"breaking"`
					NonBreaking   int `json:"non_breaking"`
					Informational int `json:"informational"`
				}{brk, nb, info},
			}
		}
		code := 0
		if len(impact) > 0 {
			code = 1
		}
		exitJSON(out, code)
		return
	}

	fmt.Printf("Direct impact on module.%s in %s (%d issue(s)):\n", moduleCallName, workspace, len(impact))
	if len(impact) == 0 {
		fmt.Println("  (none — the parent's current usage is compatible with the new version)")
	} else {
		for _, e := range impact {
			fmt.Printf("  %s\n", e)
		}
	}

	// (2) Full API diff, when we have both versions.
	if oldChildFound {
		changes := diff.Diff(oldChild.Module, newChild)
		fmt.Println()
		fmt.Printf("Module API changes old (%s) → new (%s):\n", oldChild.Dir, newVersionPath)
		if len(changes) == 0 {
			fmt.Println("  (no changes detected)")
		} else {
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
			printWhatifSection := func(title string, list []diff.Change) {
				if len(list) == 0 {
					return
				}
				fmt.Printf("  %s (%d):\n", title, len(list))
				for _, c := range list {
					fmt.Printf("    %s: %s\n", c.Subject, c.Detail)
				}
			}
			printWhatifSection("Breaking", breaking)
			printWhatifSection("Non-breaking", nonBreaking)
			printWhatifSection("Informational", info)
		}
	}

	if len(impact) > 0 {
		os.Exit(1)
	}
}

// ---- branch mode ------------------------------------------------------

// runWhatifBranch simulates merging the working tree's module upgrades
// against callers at baseRef. If only is non-empty it restricts to that
// one call name; otherwise every call that differs is simulated.
func runWhatifBranch(cmd *cobra.Command, workspace, baseRef, only string) error {
	newProj, err := loadProject(cmd, workspace)
	if err != nil {
		return fmt.Errorf("loading workspace: %w", err)
	}
	oldProj, cleanup, err := loadOldProjectForBranch(cmd, workspace, baseRef)
	if err != nil {
		return err
	}
	defer cleanup()

	pairs := pairModuleCalls(oldProj, newProj)
	if only != "" {
		filtered := pairs[:0]
		for _, p := range pairs {
			if p.key == only || p.localName == only {
				filtered = append(filtered, p)
			}
		}
		pairs = filtered
		if len(pairs) == 0 {
			return fmt.Errorf("no module call named %q differs between %s and the workspace (or call does not exist)", only, baseRef)
		}
	}

	var calls []whatifCallResult
	totalImpact := 0
	for _, p := range pairs {
		// whatif is only meaningful for calls that existed at base — we
		// need an "old parent" that called an "old child" to diff
		// against the new child. Added calls have no base-side caller.
		if p.status == statusAdded {
			continue
		}
		r := buildWhatifCallResult(p)
		totalImpact += len(r.directImpact)
		calls = append(calls, r)
	}

	if outputJSON(cmd) {
		exitJSON(whatifBranchJSONPayload(baseRef, workspace, calls), exitCodeFor(totalImpact))
		return nil
	}

	printWhatifBranchResults(baseRef, workspace, calls)
	if totalImpact > 0 {
		os.Exit(1)
	}
	return nil
}

type whatifCallResult struct {
	pair         modulePair
	directImpact []analysis.ValidationError
	apiChanges   []diff.Change // empty when we cannot compute (removed or missing child)
}

func buildWhatifCallResult(p modulePair) whatifCallResult {
	r := whatifCallResult{pair: p}
	// Direct impact: does the old parent's usage break under the new
	// child's API? For "removed" calls there's no new child, so we can
	// only note the removal.
	if p.status == statusRemoved {
		return r
	}
	if p.newNode == nil || p.oldParent == nil {
		// No child API available OR the old side doesn't have a
		// parent to cross-validate against (e.g. nested call's parent
		// was itself added in the new tree).
		if p.oldNode != nil && p.newNode != nil {
			r.apiChanges = diff.Diff(p.oldNode.Module, p.newNode.Module)
		}
		return r
	}
	r.directImpact = loader.CrossValidateCall(p.oldParent.Module, p.localName, p.newNode.Module)
	if p.oldNode != nil {
		r.apiChanges = diff.Diff(p.oldNode.Module, p.newNode.Module)
	}
	return r
}

// ---- text rendering ----

func printWhatifBranchResults(baseRef, workspace string, calls []whatifCallResult) {
	if len(calls) == 0 {
		fmt.Printf("No upgraded module calls to simulate (workspace vs %s).\n", baseRef)
		return
	}
	for i, r := range calls {
		if i > 0 {
			fmt.Println()
		}
		printOneWhatifCall(workspace, r)
	}
}

func printOneWhatifCall(workspace string, r whatifCallResult) {
	if r.pair.status == statusRemoved {
		fmt.Printf("module.%s: REMOVED (was source=%s, version=%q)\n",
			r.pair.key, r.pair.oldSource, r.pair.oldVersion)
		return
	}
	fmt.Printf("Direct impact on module.%s in %s (%d issue(s)):\n",
		r.pair.key, workspace, len(r.directImpact))
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
	fmt.Printf("  API changes for module.%s:\n", r.pair.key)
	section := func(title string, list []diff.Change) {
		if len(list) == 0 {
			return
		}
		fmt.Printf("    %s (%d):\n", title, len(list))
		for _, c := range list {
			fmt.Printf("      %s: %s\n", c.Subject, c.Detail)
		}
	}
	section("Breaking", breaking)
	section("Non-breaking", nonBreaking)
	section("Informational", info)
}

// ---- JSON rendering ----

type whatifBranchJSON struct {
	BaseRef   string                 `json:"base_ref"`
	Workspace string                 `json:"workspace"`
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

func whatifBranchJSONPayload(baseRef, workspace string, calls []whatifCallResult) whatifBranchJSON {
	out := whatifBranchJSON{BaseRef: baseRef, Workspace: workspace}
	for _, r := range calls {
		entry := whatifCallJSON{
			Name:   r.pair.key,
			Status: statusString(r.pair.status),
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
