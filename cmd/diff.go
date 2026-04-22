package cmd

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/diff"
)

var diffCmd = &cobra.Command{
	Use:   "diff [<old> <new>]",
	Short: "Compare two module versions and report breaking changes",
	Long: `Diff classifies every detected change as:
  - Breaking: existing callers or state will be affected
  - NonBreaking: safe to upgrade through
  - Informational: operational or cosmetic, but worth surfacing

Exits non-zero when any Breaking changes exist (suitable for CI gating).

Two modes:

  tflens diff <old> <new>           Explicit: compare two module directories.
  tflens diff --branch <base> [ws]  Branch: compare every module call in ws
                                    (default cwd) against its counterpart at
                                    git ref <base>. Reports per-call diffs
                                    plus added/removed calls.`,
	Args: cobra.RangeArgs(0, 2),
	RunE: func(cmd *cobra.Command, args []string) error {
		base, _ := cmd.Flags().GetString("branch")
		if base != "" {
			if len(args) > 1 {
				return fmt.Errorf("--branch mode takes at most one positional arg (workspace); got %d", len(args))
			}
			ws := "."
			if len(args) == 1 {
				ws = args[0]
			}
			if base == BranchAutoKeyword {
				auto, err := resolveAutoBase(ws)
				if err != nil {
					return err
				}
				base = auto
			}
			return runDiffBranch(cmd, ws, base)
		}
		if len(args) != 2 {
			return fmt.Errorf("diff requires <old> <new>, or --branch <base> [workspace]")
		}
		runDiff(cmd, args[0], args[1])
		return nil
	},
}

func init() {
	diffCmd.Flags().String("branch", "",
		"compare the workspace's module calls against git ref <base>; pass 'auto' to detect (@{upstream} → origin/HEAD → main → master)")
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

// ---- branch mode ------------------------------------------------------

func runDiffBranch(cmd *cobra.Command, workspace, baseRef string) error {
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
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].key < pairs[j].key })

	results := make([]branchModuleResult, 0, len(pairs))
	totalBreaking := 0
	for _, p := range pairs {
		r := branchModuleResult{pair: p}
		if p.status == statusChanged && p.oldNode != nil && p.newNode != nil {
			r.changes = diff.Diff(p.oldNode.Module, p.newNode.Module)
			for _, c := range r.changes {
				if c.Kind == diff.Breaking {
					totalBreaking++
				}
			}
		}
		results = append(results, r)
	}

	if outputJSON(cmd) {
		exitJSON(buildBranchJSON(baseRef, workspace, results), exitCodeFor(totalBreaking))
		return nil
	}

	printBranchResults(baseRef, results)
	if totalBreaking > 0 {
		os.Exit(1)
	}
	return nil
}

// branchModuleResult is the per-module-call result of a branch diff.
// For added/removed calls, changes is empty — they're reported structurally.
type branchModuleResult struct {
	pair    modulePair
	changes []diff.Change
}

func (r branchModuleResult) hasContentChanges() bool { return len(r.changes) > 0 }

func (r branchModuleResult) attrsChanged() bool {
	return r.pair.oldSource != r.pair.newSource || r.pair.oldVersion != r.pair.newVersion
}

func (r branchModuleResult) interesting() bool {
	switch r.pair.status {
	case statusAdded, statusRemoved:
		return true
	default:
		return r.hasContentChanges() || r.attrsChanged()
	}
}

func exitCodeFor(breaking int) int {
	if breaking > 0 {
		return 1
	}
	return 0
}

// ---- text rendering ----

func printBranchResults(baseRef string, results []branchModuleResult) {
	any := false
	for _, r := range results {
		if !r.interesting() {
			continue
		}
		if any {
			fmt.Println()
		}
		any = true
		printOneBranchResult(r)
	}
	if !any {
		fmt.Printf("No module-call changes detected vs %s.\n", baseRef)
	}
}

func printOneBranchResult(r branchModuleResult) {
	switch r.pair.status {
	case statusAdded:
		fmt.Printf("Module %q: ADDED (source=%s", r.pair.key, r.pair.newSource)
		if r.pair.newVersion != "" {
			fmt.Printf(", version=%s", r.pair.newVersion)
		}
		fmt.Println(")")
		return
	case statusRemoved:
		fmt.Printf("Module %q: REMOVED (was source=%s", r.pair.key, r.pair.oldSource)
		if r.pair.oldVersion != "" {
			fmt.Printf(", version=%s", r.pair.oldVersion)
		}
		fmt.Println(")")
		return
	}

	// changed
	fmt.Printf("Module %q:", r.pair.key)
	if r.pair.oldSource != r.pair.newSource {
		fmt.Printf(" source %s → %s", r.pair.oldSource, r.pair.newSource)
	}
	if r.pair.oldVersion != r.pair.newVersion {
		sep := " "
		if r.pair.oldSource != r.pair.newSource {
			sep = ", "
		}
		fmt.Printf("%sversion %q → %q", sep, r.pair.oldVersion, r.pair.newVersion)
	}
	if !r.attrsChanged() {
		fmt.Printf(" (content changed)")
	}
	fmt.Println()

	if len(r.changes) == 0 {
		fmt.Println("  (no API changes)")
		return
	}
	var breaking, nonBreaking, info []diff.Change
	for _, c := range r.changes {
		switch c.Kind {
		case diff.Breaking:
			breaking = append(breaking, c)
		case diff.NonBreaking:
			nonBreaking = append(nonBreaking, c)
		case diff.Informational:
			info = append(info, c)
		}
	}
	if len(breaking) > 0 {
		fmt.Printf("  Breaking (%d):\n", len(breaking))
		for _, c := range breaking {
			fmt.Printf("    %s: %s\n", c.Subject, c.Detail)
		}
	}
	if len(nonBreaking) > 0 {
		fmt.Printf("  Non-breaking (%d):\n", len(nonBreaking))
		for _, c := range nonBreaking {
			fmt.Printf("    %s: %s\n", c.Subject, c.Detail)
		}
	}
	if len(info) > 0 {
		fmt.Printf("  Informational (%d):\n", len(info))
		for _, c := range info {
			fmt.Printf("    %s: %s\n", c.Subject, c.Detail)
		}
	}
}

// ---- JSON rendering ----

func buildBranchJSON(baseRef, workspace string, results []branchModuleResult) any {
	out := branchJSON{BaseRef: baseRef, Workspace: workspace}
	for _, r := range results {
		if !r.interesting() {
			continue
		}
		entry := branchModuleJSON{
			Name:       r.pair.key,
			Status:     statusString(r.pair.status),
			OldSource:  r.pair.oldSource,
			OldVersion: r.pair.oldVersion,
			NewSource:  r.pair.newSource,
			NewVersion: r.pair.newVersion,
		}
		for _, c := range r.changes {
			entry.Changes = append(entry.Changes, toJSONChange(c))
			switch c.Kind {
			case diff.Breaking:
				entry.Summary.Breaking++
				out.Summary.Breaking++
			case diff.NonBreaking:
				entry.Summary.NonBreaking++
				out.Summary.NonBreaking++
			case diff.Informational:
				entry.Summary.Informational++
				out.Summary.Informational++
			}
		}
		out.Modules = append(out.Modules, entry)
	}
	return out
}

type branchJSON struct {
	BaseRef   string             `json:"base_ref"`
	Workspace string             `json:"workspace"`
	Modules   []branchModuleJSON `json:"modules"`
	Summary   branchSummaryJSON  `json:"summary"`
}

type branchModuleJSON struct {
	Name       string            `json:"name"`
	Status     string            `json:"status"`
	OldSource  string            `json:"old_source,omitempty"`
	OldVersion string            `json:"old_version,omitempty"`
	NewSource  string            `json:"new_source,omitempty"`
	NewVersion string            `json:"new_version,omitempty"`
	Changes    []jsonChange      `json:"changes,omitempty"`
	Summary    branchSummaryJSON `json:"summary"`
}

type branchSummaryJSON struct {
	Breaking      int `json:"breaking"`
	NonBreaking   int `json:"non_breaking"`
	Informational int `json:"informational"`
}

func statusString(s moduleCallStatus) string {
	switch s {
	case statusAdded:
		return "added"
	case statusRemoved:
		return "removed"
	default:
		return "changed"
	}
}

