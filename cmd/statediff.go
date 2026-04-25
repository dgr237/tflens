package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/config"
	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/render"
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
		s := config.FromCommand(cmd, config.WithPath(pathArg(args, 0)))
		if err := resolveAutoBaseRef(&s); err != nil {
			return err
		}
		return runStatediff(s)
	},
}

func init() {
	statediffCmd.Flags().String("ref", config.RefAutoKeyword,
		"git ref to compare against (branch, tag, SHA, …); 'auto' detects @{upstream} → origin/HEAD → main → master")
	statediffCmd.Flags().String("state", "", "optional Terraform state v4 JSON file for instance cross-reference")
	rootCmd.AddCommand(statediffCmd)
}

func runStatediff(s config.Settings) error {
	oldProj, newProj, cleanup, err := loader.New(s).ProjectsForDiff(s.Path, s.BaseRef)
	if err != nil {
		return err
	}
	defer cleanup()
	state, err := loadOptionalState(s.StatePath)
	if err != nil {
		return err
	}
	result := statediff.Analyze(oldProj, newProj, state)
	result.BaseRef, result.Path = s.BaseRef, s.Path
	render.New(s).Statediff(&result)
	if result.FlaggedCount() > 0 {
		os.Exit(1)
	}
	return nil
}

// loadOptionalState parses the Terraform state at path, or returns
// (nil, nil) when path is empty (the --state flag wasn't supplied).
func loadOptionalState(path string) (*tfstate.State, error) {
	if path == "" {
		return nil, nil
	}
	state, err := tfstate.Parse(path)
	if err != nil {
		return nil, fmt.Errorf("loading state: %w", err)
	}
	return state, nil
}
