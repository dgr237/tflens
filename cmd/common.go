package cmd

import (
	"fmt"
	"os"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/loader"
)

// mustLoadModule loads a single .tf file or a directory of .tf files
// via loader.LoadAny. File-level parse errors are printed as warnings
// to stderr; a top-level I/O failure (missing path, unreadable inode)
// is fatal. The returned module may be nil only when LoadAny itself
// errored — file-level partial-parse results still produce a usable
// module.
func mustLoadModule(path string) *analysis.Module {
	mod, fileErrs, err := loader.LoadAny(path)
	if err != nil {
		fatalf("%v", err)
	}
	for _, fe := range fileErrs {
		fmt.Fprintf(os.Stderr, "warning: parse errors in %s\n", fe.Path)
		for _, e := range fe.Errors {
			fmt.Fprintf(os.Stderr, "  %s\n", e)
		}
	}
	if mod == nil {
		os.Exit(1)
	}
	return mod
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
