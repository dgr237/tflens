package render

import (
	"io"

	"github.com/hashicorp/hcl/v2"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/statediff"
)

// SingleModuleRenderer covers the subcommands that operate on one
// already-loaded module (cycles / deps / impact / inventory / unused).
type SingleModuleRenderer interface {
	Cycles(cycles [][]string)
	Deps(id string, deps, dependents []string)
	Impact(id string, affected []string)
	Inventory(m *analysis.Module)
	Unused(unused []analysis.Entity)
}

// ValidateRenderer covers the validate subcommand.
type ValidateRenderer interface {
	Validate(
		refErrs, crossErrs []analysis.ValidationError,
		typeErrs []analysis.TypeCheckError,
	)
}

// CacheRenderer covers the cache info / clear subcommands. Both the
// "already empty" and "cleared" outputs share the renderer because
// they're conceptually the same surface (the result of a cache clear
// attempt).
type CacheRenderer interface {
	CacheInfo(path string, entries int, bytes int64)
	CacheAlreadyEmpty(path string)
	CacheCleared(entries int, bytes int64, path string)
}

// DiffRenderer covers the ref-comparing subcommands (diff / whatif /
// statediff). Each receives the path + baseRef alongside the analysed
// result so the JSON envelope can include them.
type DiffRenderer interface {
	Diff(baseRef, path string, results []diff.PairResult, rootChanges []diff.Change)
	Whatif(baseRef, path string, calls []diff.WhatifResult)
	Statediff(result *statediff.Result)
}

// FmtRenderer covers the fmt subcommand's parse-error surface. Note
// that fmt's actual formatted output (or --check / --write side
// effects) is intentionally NOT routed through the renderer — that's
// raw HCL bytes, not a rendered view.
type FmtRenderer interface {
	FmtParseErrors(diags hcl.Diagnostics)
}

// Renderer is the composite that both ConsoleRenderer and
// JSONRenderer satisfy. cmd subcommands hold a Renderer (or one of
// the domain interfaces above) and call the relevant method without
// branching on s.JSON.
type Renderer interface {
	SingleModuleRenderer
	ValidateRenderer
	CacheRenderer
	DiffRenderer
	FmtRenderer
}

// New returns the renderer matching s.JSON / cmd's chosen output
// format. w is where the rendered output goes — typically os.Stdout
// for the success path; cmd swaps to os.Stderr for the validate
// "errors found" branch.
func New(jsonMode bool, w io.Writer) Renderer {
	if jsonMode {
		return &JSONRenderer{W: w}
	}
	return &ConsoleRenderer{W: w}
}
