package render

import (
	"io"

	"github.com/hashicorp/hcl/v2"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/statediff"
)

// ConsoleRenderer is the human-readable Renderer implementation —
// every method delegates to the corresponding WriteX helper. Kept as
// a thin pass-through so the WriteX functions remain individually
// callable + testable from outside the renderer abstraction (the
// existing tests target them directly).
type ConsoleRenderer struct {
	W io.Writer
}

func (c *ConsoleRenderer) Cycles(cycles [][]string)                    { WriteCycles(c.W, cycles) }
func (c *ConsoleRenderer) Deps(id string, deps, dependents []string)   { WriteDeps(c.W, id, deps, dependents) }
func (c *ConsoleRenderer) Impact(id string, affected []string)         { WriteImpact(c.W, id, affected) }
func (c *ConsoleRenderer) Inventory(m *analysis.Module)                { WriteInventory(c.W, m) }
func (c *ConsoleRenderer) Unused(unused []analysis.Entity)             { WriteUnused(c.W, unused) }
func (c *ConsoleRenderer) FmtParseErrors(diags hcl.Diagnostics)        { WriteFmtParseErrors(c.W, diags) }
func (c *ConsoleRenderer) CacheInfo(path string, entries int, b int64) { WriteCacheInfo(c.W, path, entries, b) }
func (c *ConsoleRenderer) CacheAlreadyEmpty(path string)               { WriteCacheAlreadyEmpty(c.W, path) }
func (c *ConsoleRenderer) CacheCleared(entries int, b int64, path string) {
	WriteCacheCleared(c.W, entries, b, path)
}

func (c *ConsoleRenderer) Validate(
	refErrs, crossErrs []analysis.ValidationError,
	typeErrs []analysis.TypeCheckError,
) {
	WriteValidate(c.W, refErrs, crossErrs, typeErrs)
}

func (c *ConsoleRenderer) Diff(
	baseRef, _ string,
	results []diff.PairResult,
	rootChanges []diff.Change,
) {
	WriteDiffResults(c.W, baseRef, results, rootChanges)
}

func (c *ConsoleRenderer) Whatif(baseRef, path string, calls []diff.WhatifResult) {
	WriteWhatifResults(c.W, baseRef, path, calls)
}

func (c *ConsoleRenderer) Statediff(result *statediff.Result) {
	WriteStatediff(c.W, result)
}
