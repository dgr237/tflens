package cmd

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/parser"
	"github.com/dgr237/tflens/pkg/printer"
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

NOTE: the current lexer discards comments, so formatting is a lossy
round-trip. Do not use -w on code with comments you want to keep until
comment preservation lands.`,
	Args: cobra.ExactArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		runFmt(cmd, args[0])
	},
}

func init() {
	fmtCmd.Flags().BoolP("write", "w", false, "rewrite the file in place")
	fmtCmd.Flags().Bool("check", false, "exit non-zero if the file is not already formatted")
	rootCmd.AddCommand(fmtCmd)
}

func runFmt(cmd *cobra.Command, path string) {
	write, _ := cmd.Flags().GetBool("write")
	check, _ := cmd.Flags().GetBool("check")
	if write && check {
		fatalf("--write and --check are mutually exclusive")
	}

	info, err := os.Stat(path)
	if err != nil {
		fatalf("%v", err)
	}
	if info.IsDir() {
		fatalf("fmt operates on individual files; use a .tf path")
	}
	src, err := os.ReadFile(path)
	if err != nil {
		fatalf("reading file: %v", err)
	}
	file, errs := parser.ParseFile(src, path)
	for _, e := range errs {
		fmt.Fprintf(os.Stderr, "parse error: %s\n", e)
	}
	if len(errs) > 0 {
		os.Exit(1)
	}
	formatted := printer.Print(file)

	switch {
	case check:
		if string(src) != formatted {
			fmt.Println(path)
			os.Exit(1)
		}
	case write:
		if string(src) == formatted {
			return // no-op; don't bump mtime unnecessarily
		}
		if err := os.WriteFile(path, []byte(formatted), info.Mode()); err != nil {
			fatalf("writing %s: %v", path, err)
		}
	default:
		fmt.Print(formatted)
	}
}
