package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/statediff"
	"github.com/dgr237/tflens/pkg/tfstate"
)

var statediffCmd = &cobra.Command{
	Use:   "statediff [path]",
	Short: "Identify resources a PR may create, destroy, or re-instance",
	Long: `statediff compares the working tree in path (default cwd) against
a git ref at the resource identity level and surfaces changes that may
alter Terraform state when the PR is merged:

  1. Resource declarations added or removed between the two trees.
  2. Locals or variable defaults whose value expression changed AND
     whose dependency chain reaches a count or for_each meta-argument —
     the common way a seemingly-small edit silently destroys instances.
  3. Renames declared via ` + "`moved {}`" + ` blocks, recognised so the same
     resource under a new name is not double-reported as add + remove.
  4. When --state <file> is given: for every flagged resource, the
     instances currently in state (so a reviewer can see the concrete
     addresses that may be affected).

Exits non-zero when anything is flagged (renames and state orphans do
not count). Suitable for CI gating.

The ref defaults to 'auto', which resolves to @{upstream} → origin/HEAD
→ main → master.

What it does NOT do: attribute-level plan simulation. That needs
provider schemas and expression evaluation — run 'terraform plan' for
that. statediff is a static hazard detector, not a plan replacement.`,
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
		statePath, _ := cmd.Flags().GetString("state")
		return runStatediff(cmd, path, base, statePath)
	},
}

func init() {
	statediffCmd.Flags().String("ref", RefAutoKeyword,
		"git ref to compare against (branch, tag, SHA, …); 'auto' detects @{upstream} → origin/HEAD → main → master")
	statediffCmd.Flags().String("state", "", "optional Terraform state v4 JSON file for instance cross-reference")
	rootCmd.AddCommand(statediffCmd)
}

func runStatediff(cmd *cobra.Command, path, baseRef, statePath string) error {
	newProj, err := loadProject(cmd, path)
	if err != nil {
		return fmt.Errorf("loading path: %w", err)
	}
	oldProj, cleanup, err := loadOldProjectForRef(cmd, path, baseRef)
	if err != nil {
		return err
	}
	defer cleanup()

	var state *tfstate.State
	if statePath != "" {
		state, err = tfstate.Parse(statePath)
		if err != nil {
			return fmt.Errorf("loading state: %w", err)
		}
	}

	result := statediff.Analyze(oldProj, newProj, state)
	result.BaseRef = baseRef
	result.Path = path

	if outputJSON(cmd) {
		exitJSON(result, diff.ExitCodeFor(result.FlaggedCount()))
		return nil
	}
	printStatediff(&result)
	if result.FlaggedCount() > 0 {
		os.Exit(1)
	}
	return nil
}

// ---- text rendering ----

func printStatediff(r *statediff.Result) {
	any := false

	if len(r.AddedResources) > 0 || len(r.RemovedResources) > 0 {
		any = true
		fmt.Printf("Resource identity changes vs %s:\n", r.BaseRef)
		for _, a := range r.AddedResources {
			fmt.Printf("  + %s (%s)\n", a.Address(), a.Mode)
		}
		for _, a := range r.RemovedResources {
			fmt.Printf("  - %s (%s)\n", a.Address(), a.Mode)
		}
	}

	if len(r.RenamedResources) > 0 {
		if any {
			fmt.Println()
		}
		any = true
		fmt.Println("Renames (moved block handled — no destroy/recreate):")
		for _, rn := range r.RenamedResources {
			fmt.Printf("  %s → %s\n", rn.FromAddress(), rn.ToAddress())
		}
	}

	if len(r.SensitiveChanges) > 0 {
		if any {
			fmt.Println()
		}
		any = true
		fmt.Println("Value changes that may alter count/for_each expansion:")
		for _, sc := range r.SensitiveChanges {
			prefix := sc.Kind + "." + sc.Name
			if sc.Module != "" {
				prefix = sc.Module + "." + prefix
			}
			fmt.Printf("  - %s\n", prefix)
			fmt.Printf("      old: %s\n", orDash(sc.OldValue))
			fmt.Printf("      new: %s\n", orDash(sc.NewValue))
			for _, ar := range sc.AffectedResources {
				fmt.Printf("    Affected: %s (%s)\n", ar.Address(), ar.MetaArg)
				for _, inst := range ar.StateInstances {
					fmt.Printf("      • state instance: %s\n", inst)
				}
			}
		}
	}

	if len(r.StateOrphans) > 0 {
		if any {
			fmt.Println()
		}
		fmt.Println("State drift — addresses in state but not declared in the new tree:")
		for _, o := range r.StateOrphans {
			fmt.Printf("  ? %s\n", o)
		}
	}

	if !any && len(r.StateOrphans) == 0 {
		fmt.Printf("No resource identity or sensitive-local changes detected vs %s.\n", r.BaseRef)
	}
}

func orDash(s string) string {
	if s == "" {
		return "(absent)"
	}
	return s
}
