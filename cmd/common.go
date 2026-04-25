package cmd

import (
	"fmt"
	"os"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/config"
	"github.com/dgr237/tflens/pkg/loader"
)

// pathArg returns args[i] if present, otherwise ".". Used by every
// subcommand whose first positional arg is an optional workspace path.
func pathArg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return "."
}

// resolveAutoBaseRef rewrites s.BaseRef in place when it equals the
// "auto" keyword: dispatches to loader.ResolveAutoRef using s.Path so
// the auto detection runs against the correct workspace. No-op when
// the user supplied an explicit ref.
func resolveAutoBaseRef(s *config.Settings) error {
	if s.BaseRef != config.RefAutoKeyword {
		return nil
	}
	auto, err := loader.ResolveAutoRef(s.Path)
	if err != nil {
		return err
	}
	s.BaseRef = auto
	return nil
}

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
	printFileErrs(fileErrs)
	if mod == nil {
		os.Exit(1)
	}
	return mod
}

func fatalf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "error: "+format+"\n", args...)
	os.Exit(1)
}
