package cmd

import (
	"fmt"
	"os"
	"sort"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
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

	pairs := pairModuleCalls(oldProj, newProj)
	sort.Slice(pairs, func(i, j int) bool { return pairs[i].key < pairs[j].key })

	results := make([]refModuleResult, 0, len(pairs))
	totalBreaking := 0
	for _, p := range pairs {
		r := refModuleResult{pair: p}
		if p.status == statusChanged && p.oldNode != nil && p.newNode != nil {
			// Local-source children are owned by this repo: their API is
			// implementation detail and only the parent's consumption
			// matters. External (registry/git) children come from a
			// publisher who's responsible for breaking-change discipline,
			// so we surface every API change.
			if resolver.IsLocalSource(p.newSource) {
				r.changes = consumptionChangesForLocal(p)
			} else {
				r.changes = diff.Diff(p.oldNode.Module, p.newNode.Module)
			}
			for _, c := range r.changes {
				if c.Kind == diff.Breaking {
					totalBreaking++
				}
			}
		}
		results = append(results, r)
	}

	if outputJSON(cmd) {
		exitJSON(buildRefJSON(baseRef, path, results), exitCodeFor(totalBreaking))
		return nil
	}

	printRefResults(baseRef, results)
	if totalBreaking > 0 {
		os.Exit(1)
	}
	return nil
}

// consumptionChangesForLocal turns cross_validate findings against the
// new parent + new child into diff.Change entries. Used in place of
// diff.Diff for local-source ("internal") children, where the child's
// API is implementation detail and only the parent's consumption is
// observable.
//
// Returns an empty slice when the parent's usage is consistent — i.e.
// every required child variable is passed, no unknown args, types
// compatible, and every module.<name>.<output> reference still resolves.
func consumptionChangesForLocal(p modulePair) []diff.Change {
	if p.newParent == nil {
		return nil
	}
	cvErrs := loader.CrossValidateCall(p.newParent.Module, p.localName, p.newNode.Module)
	if len(cvErrs) == 0 {
		return nil
	}
	out := make([]diff.Change, 0, len(cvErrs))
	for _, e := range cvErrs {
		out = append(out, diff.Change{
			Kind:    diff.Breaking,
			Subject: e.EntityID,
			Detail:  e.Msg,
			NewPos:  e.Pos,
		})
	}
	return out
}

// refModuleResult is the per-module-call result of a branch diff.
// For added/removed calls, changes is empty — they're reported structurally.
type refModuleResult struct {
	pair    modulePair
	changes []diff.Change
}

func (r refModuleResult) hasContentChanges() bool { return len(r.changes) > 0 }

func (r refModuleResult) attrsChanged() bool {
	return r.pair.oldSource != r.pair.newSource || r.pair.oldVersion != r.pair.newVersion
}

func (r refModuleResult) interesting() bool {
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

func printRefResults(baseRef string, results []refModuleResult) {
	any := false
	for _, r := range results {
		if !r.interesting() {
			continue
		}
		if any {
			fmt.Println()
		}
		any = true
		printOneRefResult(r)
	}
	if !any {
		fmt.Printf("No module-call changes detected vs %s.\n", baseRef)
	}
}

func printOneRefResult(r refModuleResult) {
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

func buildRefJSON(baseRef, path string, results []refModuleResult) any {
	out := refJSON{BaseRef: baseRef, Path: path}
	for _, r := range results {
		if !r.interesting() {
			continue
		}
		entry := refModuleJSON{
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

type refJSON struct {
	BaseRef   string             `json:"base_ref"`
	Path string             `json:"path"`
	Modules   []refModuleJSON `json:"modules"`
	Summary   refSummaryJSON  `json:"summary"`
}

type refModuleJSON struct {
	Name       string            `json:"name"`
	Status     string            `json:"status"`
	OldSource  string            `json:"old_source,omitempty"`
	OldVersion string            `json:"old_version,omitempty"`
	NewSource  string            `json:"new_source,omitempty"`
	NewVersion string            `json:"new_version,omitempty"`
	Changes    []jsonChange      `json:"changes,omitempty"`
	Summary    refSummaryJSON `json:"summary"`
}

type refSummaryJSON struct {
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

