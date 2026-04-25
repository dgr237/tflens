package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/config"
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
		opts := []config.Option{config.WithPath(pathArg(args, 0))}
		if len(args) == 2 {
			opts = append(opts, config.WithOnlyName(args[1]))
		}
		s := config.FromCommand(cmd, opts...)
		if err := resolveAutoBaseRef(&s); err != nil {
			return err
		}
		return runWhatifRef(s)
	},
}

func init() {
	whatifCmd.Flags().String("ref", config.RefAutoKeyword,
		"git ref to compare against (branch, tag, SHA, …); 'auto' detects @{upstream} → origin/HEAD → main → master")
	rootCmd.AddCommand(whatifCmd)
}

// runWhatifRef simulates merging the working tree's module upgrades
// against callers at s.BaseRef. If s.OnlyName is non-empty it
// restricts to that one call name; otherwise every call that differs
// is simulated.
func runWhatifRef(s config.Settings) error {
	oldProj, newProj, cleanup, err := loader.New(s).ProjectsForDiff(s.Path, s.BaseRef)
	if err != nil {
		return err
	}
	defer cleanup()
	calls, totalImpact, filtered := diff.AnalyzeWhatif(oldProj, newProj, s.OnlyName)
	if filtered {
		return fmt.Errorf("no module call named %q differs between %s and the path (or call does not exist)", s.OnlyName, s.BaseRef)
	}
	render.New(s).Whatif(s.BaseRef, s.Path, calls)
	if totalImpact > 0 {
		os.Exit(1)
	}
	return nil
}
