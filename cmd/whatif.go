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

Exits non-zero when the parent would break.`,
	Args: cobra.ExactArgs(3),
	Run: func(cmd *cobra.Command, args []string) {
		runWhatif(cmd, args[0], args[1], args[2])
	},
}

func init() {
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
	project, fileErrs, err := loader.LoadProject(workspace)
	if err != nil {
		fatalf("loading workspace: %v", err)
	}
	for _, fe := range fileErrs {
		fmt.Fprintf(os.Stderr, "warning: parse errors in %s\n", fe.Path)
		for _, e := range fe.Errors {
			fmt.Fprintf(os.Stderr, "  %s\n", e)
		}
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
