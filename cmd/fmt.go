package cmd

import (
	"fmt"
	"os"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/config"
	"github.com/dgr237/tflens/pkg/render"
)

var fmtCmd = &cobra.Command{
	Use:   "fmt <file.tf>",
	Short: "Print normalised HCL (or rewrite with -w, or check with --check)",
	Long: `fmt parses the file and prints its normalised form.

Without flags, the formatted output is written to stdout and the file on disk
is unchanged. With -w the file is rewritten in place. With --check the file
is compared to its normalised form — the command is silent and exits 0 when
already formatted, or prints the path and exits 1 when not formatted
(suitable for CI gating).

Comments and blank lines are preserved.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		s := config.FromCommand(cmd)
		s.Path = args[0]
		runFmt(s)
	},
}

func init() {
	fmtCmd.Flags().BoolP("write", "w", false, "rewrite the file in place")
	fmtCmd.Flags().Bool("check", false, "exit non-zero if the file is not already formatted")
	rootCmd.AddCommand(fmtCmd)
}

func runFmt(s config.Settings) {
	if s.Write && s.Check {
		fatalf("--write and --check are mutually exclusive")
	}

	info, err := os.Stat(s.Path)
	if err != nil {
		fatalf("%v", err)
	}
	if info.IsDir() {
		fatalf("fmt operates on individual files; use a .tf path")
	}
	src, err := os.ReadFile(s.Path)
	if err != nil {
		fatalf("reading file: %v", err)
	}

	// Parse first to surface syntax errors with positions; the formatter
	// will silently produce garbage on broken input.
	p := hclparse.NewParser()
	if _, diags := p.ParseHCL(src, s.Path); diags.HasErrors() {
		// JSON path goes to stdout (machine-readable envelope);
		// text path stays on stderr to keep it out of pipes.
		w := os.Stderr
		if s.JSON {
			w = os.Stdout
		}
		render.New(s.JSON, w).FmtParseErrors(diags)
		os.Exit(1)
	}

	formatted := string(hclwrite.Format(src))

	switch {
	case s.Check:
		if string(src) != formatted {
			fmt.Println(s.Path)
			os.Exit(1)
		}
	case s.Write:
		if string(src) == formatted {
			return // no-op; don't bump mtime unnecessarily
		}
		if err := os.WriteFile(s.Path, []byte(formatted), info.Mode()); err != nil {
			fatalf("writing %s: %v", s.Path, err)
		}
	default:
		fmt.Print(formatted)
	}
}
