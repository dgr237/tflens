package cmd

import (
	"fmt"
	"os"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/parser"
)

// mustLoadModule loads a single-file or single-directory module. Exits on I/O
// or parse errors (partial parse results are printed but treated as fatal).
func mustLoadModule(path string) *analysis.Module {
	info, err := os.Stat(path)
	if err != nil {
		fatalf("%v", err)
	}
	if info.IsDir() {
		mod, fileErrs, err := loader.LoadDir(path)
		if err != nil {
			fatalf("loading directory: %v", err)
		}
		for _, fe := range fileErrs {
			fmt.Fprintf(os.Stderr, "warning: parse errors in %s\n", fe.Path)
			for _, e := range fe.Errors {
				fmt.Fprintf(os.Stderr, "  %s\n", e)
			}
		}
		return mod
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
	return analysis.Analyse(file)
}

// mustEntityExists exits with a helpful message when id isn't declared in mod.
func mustEntityExists(mod *analysis.Module, id, path string) {
	for _, e := range mod.Entities() {
		if e.ID() == id {
			return
		}
	}
	fatalf("entity %q not found in %s\nRun 'tflens inventory %s' to list available entities",
		id, path, path)
}

func plural(n int, singular, plural string) string {
	if n == 1 {
		return singular
	}
	return plural
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
