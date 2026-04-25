package render_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/hashicorp/hcl/v2"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/render"
	"github.com/dgr237/tflens/pkg/statediff"
	"github.com/dgr237/tflens/pkg/token"
)

// markdownCase runs one MarkdownRenderer scenario through a thunk and
// matches the captured bytes against testdata/markdown/<Name>.golden.md.
// Run takes the full render.Renderer so cases can exercise any of the
// 13 interface methods — markdown's value is in PR-comment shape, so
// we pin every surface (rich for Diff/Whatif/Statediff/Validate, terse
// for the rest).
type markdownCase struct {
	Name string
	Run  func(r render.Renderer)
}

// TestRendererMarkdownCases exercises the four primary surfaces
// through inline scenarios and matches against per-case
// testdata/markdown/<Name>.golden.md files. Update with `go test
// ./pkg/render/... -run TestRendererMarkdownCases -update`.
func TestRendererMarkdownCases(t *testing.T) {
	for _, tc := range markdownCases {
		t.Run(tc.Name, func(t *testing.T) {
			var b bytes.Buffer
			r := markdownRenderer(&b)
			tc.Run(r)
			checkMarkdownGolden(t, tc.Name, b.Bytes())
		})
	}
}

var markdownCases = []markdownCase{
	{
		// Diff baseline: no changes anywhere → emits the ✅ summary
		// line and nothing else.
		Name: "diff_no_changes",
		Run: func(r render.Renderer) {
			r.Diff("main", "./infra", nil, nil)
		},
	},
	{
		// Diff with mixed-kind root + module sections — exercises
		// severity badges, summary totals, the open=open attribute
		// on Breaking-containing details, and the fix-hint emission.
		Name: "diff_mixed_changes",
		Run: func(r render.Renderer) {
			r.Diff("main", "./infra",
				[]diff.PairResult{{
					Pair: loader.ModuleCallPair{
						Key: "vpc", Status: loader.StatusChanged,
						OldSource: "ns/vpc/aws", NewSource: "ns/vpc/aws",
						OldVersion: "1.0.0", NewVersion: "2.0.0",
					},
					Changes: []diff.Change{
						{Kind: diff.Breaking, Subject: "var.required", Detail: "removed",
							Hint: "callers passing this variable will fail"},
						{Kind: diff.NonBreaking, Subject: "var.tags", Detail: "added optional"},
						{Kind: diff.Informational, Subject: "out.docs", Detail: "description updated"},
					},
				}, {
					Pair: loader.ModuleCallPair{
						Key: "eks", Status: loader.StatusAdded,
						NewSource: "terraform-aws-modules/eks/aws", NewVersion: "20.0.0",
					},
				}},
				[]diff.Change{
					{Kind: diff.Breaking, Subject: "variable.cluster_name",
						Detail: "required variable added",
						Hint:   "add `cluster_name = ...` to the root invocation"},
				})
		},
	},
	{
		// Whatif with a direct-impact call: red prefix on the heading,
		// open=open on the details, "Direct impact" subsection, and
		// the full API diff section labelled.
		Name: "whatif_direct_impact",
		Run: func(r render.Renderer) {
			r.Whatif("main", "./infra", []diff.WhatifResult{{
				Pair: loader.ModuleCallPair{
					Key: "vpc", Status: loader.StatusChanged,
					OldVersion: "1.0.0", NewVersion: "2.0.0",
				},
				DirectImpact: []analysis.ValidationError{{
					EntityID: "module.vpc",
					Ref:      "var.removed_input",
					Msg:      "child no longer accepts argument",
				}},
				APIChanges: []diff.Change{
					{Kind: diff.Breaking, Subject: "var.removed_input", Detail: "removed"},
				},
			}})
		},
	},
	{
		// Whatif with no impact + no API changes → "(no consumer-
		// affecting changes)" baseline.
		Name: "whatif_clean_upgrade",
		Run: func(r render.Renderer) {
			r.Whatif("main", "./infra", []diff.WhatifResult{{
				Pair: loader.ModuleCallPair{
					Key: "vpc", Status: loader.StatusChanged,
					OldVersion: "1.0.0", NewVersion: "1.0.1",
				},
			}})
		},
	},
	{
		// Statediff with everything: added/removed/renamed resources +
		// a sensitive change with affected resources + state instances
		// + a state orphan. Pins the full multi-section shape.
		Name: "statediff_full",
		Run: func(r render.Renderer) {
			r.Statediff(&statediff.Result{
				BaseRef: "main",
				Path:    "./infra",
				AddedResources: []statediff.ResourceRef{
					{Type: "aws_s3_bucket", Name: "logs", Mode: "managed"},
				},
				RemovedResources: []statediff.ResourceRef{
					{Type: "aws_iam_user", Name: "old", Mode: "managed"},
				},
				RenamedResources: []statediff.RenamePair{
					{From: "resource.aws_vpc.main", To: "resource.aws_vpc.primary"},
				},
				SensitiveChanges: []statediff.SensitiveChange{{
					Kind: "local", Name: "regions",
					OldValue: `["us-east-1"]`, NewValue: `["us-east-1","us-west-2"]`,
					AffectedResources: []statediff.AffectedResource{{
						Type: "aws_subnet", Name: "per_region", MetaArg: "for_each",
						StateInstances: []string{`aws_subnet.per_region["us-east-1"]`},
					}},
				}},
				StateOrphans: []string{"aws_security_group.legacy"},
			})
		},
	},
	{
		// Statediff with no findings → ✅ baseline.
		Name: "statediff_clean",
		Run: func(r render.Renderer) {
			r.Statediff(&statediff.Result{BaseRef: "main", Path: "./infra"})
		},
	},
	{
		// Validate with a mix of error kinds → all three subsections
		// (undefined refs, cross-module, type errors) plus the
		// total-count summary line.
		Name: "validate_mixed_errors",
		Run: func(r render.Renderer) {
			r.Validate(
				[]analysis.ValidationError{{
					EntityID: "resource.aws_vpc.main",
					Ref:      "var.typo",
					Pos:      token.Position{File: "main.tf", Line: 12},
				}},
				[]analysis.ValidationError{{
					EntityID: "module.vpc",
					Msg:      "child requires `region` but root passes `regions`",
					Pos:      token.Position{File: "main.tf", Line: 5},
				}},
				[]analysis.TypeCheckError{{
					EntityID: "variable.count",
					Attr:     "default",
					Msg:      "default value `\"three\"` is string, declared type number",
					Pos:      token.Position{File: "variables.tf", Line: 3},
				}},
			)
		},
	},
	{
		// Validate clean → ✅ baseline.
		Name: "validate_clean",
		Run: func(r render.Renderer) {
			r.Validate(nil, nil, nil)
		},
	},

	// ---- Diff missing-branch coverage ----

	{
		// Removed pair gets a single short heading line, no details
		// block. Pins the StatusRemoved branch + version-suffix
		// emission inside writePairResult.
		Name: "diff_pair_removed",
		Run: func(r render.Renderer) {
			r.Diff("main", "./infra", []diff.PairResult{{
				Pair: loader.ModuleCallPair{
					Key: "vpc", Status: loader.StatusRemoved,
					OldSource: "ns/vpc/aws", OldVersion: "1.0.0",
				},
			}}, nil)
		},
	},
	{
		// Changed pair where source + version are identical and
		// Changes is empty — the pair filters as uninteresting AND
		// the no-API-changes branch in writePairResult is moot.
		// Confirms the "no changes" baseline triggers.
		Name: "diff_only_uninteresting_pair",
		Run: func(r render.Renderer) {
			r.Diff("main", "./infra", []diff.PairResult{{
				Pair: loader.ModuleCallPair{
					Key: "vpc", Status: loader.StatusChanged,
					OldSource: "x", NewSource: "x",
				},
			}}, nil)
		},
	},
	{
		// Source moved but Changes is empty — emits "(no API
		// changes)" inside the section. Hits the empty-changes
		// branch of writePairResult that diff_mixed_changes doesn't.
		Name: "diff_changed_no_api_changes",
		Run: func(r render.Renderer) {
			r.Diff("main", "./infra", []diff.PairResult{{
				Pair: loader.ModuleCallPair{
					Key: "vpc", Status: loader.StatusChanged,
					OldSource: "x", NewSource: "y",
				},
			}}, nil)
		},
	},

	// ---- Statediff missing-branch coverage ----

	{
		// nil result hits the early-return guard before FlaggedCount.
		Name: "statediff_nil_result",
		Run: func(r render.Renderer) {
			r.Statediff(nil)
		},
	},

	// ---- Secondary surfaces (full coverage of the terse impls) ----

	{
		// Cycles with a non-empty list — exercises quoteAll +
		// the multi-step cycle rendering. Single-cycle is enough
		// for the format pin.
		Name: "cycles_present",
		Run: func(r render.Renderer) {
			r.Cycles([][]string{
				{"resource.aws_a.x", "resource.aws_b.y", "resource.aws_a.x"},
			})
		},
	},
	{
		// Cycles empty → ✅ baseline.
		Name: "cycles_clean",
		Run: func(r render.Renderer) {
			r.Cycles(nil)
		},
	},
	{
		// Deps with both populated — pins the "Depends on" /
		// "Referenced by" sub-section ordering and bullet shape.
		Name: "deps_populated",
		Run: func(r render.Renderer) {
			r.Deps("resource.aws_vpc.main",
				[]string{"variable.cidr"},
				[]string{"resource.aws_subnet.a", "resource.aws_subnet.b"})
		},
	},
	{
		// Deps with both empty — pins the "_(none)_" italic markers
		// in both sub-sections.
		Name: "deps_empty",
		Run: func(r render.Renderer) {
			r.Deps("resource.aws_vpc.main", nil, nil)
		},
	},
	{
		// Impact with affected entities — exercises pluralY ("ies"
		// branch for n != 1) and the numbered-list rendering.
		Name: "impact_multiple",
		Run: func(r render.Renderer) {
			r.Impact("variable.cidr", []string{
				"resource.aws_vpc.main",
				"resource.aws_subnet.a",
			})
		},
	},
	{
		// Impact with one affected entity — pluralY's "y" branch.
		Name: "impact_single",
		Run: func(r render.Renderer) {
			r.Impact("variable.cidr", []string{"resource.aws_vpc.main"})
		},
	},
	{
		// Impact with no affected entities — italic baseline.
		Name: "impact_none",
		Run: func(r render.Renderer) {
			r.Impact("variable.unused", nil)
		},
	},
	{
		// Inventory with entities — pins the markdown-table format.
		Name: "inventory_populated",
		Run: func(r render.Renderer) {
			r.Inventory(moduleFromSrc(t__noopT(),
				`variable "x" { type = string }
resource "aws_vpc" "main" {}`))
		},
	},
	{
		// Inventory empty — italic baseline.
		Name: "inventory_empty",
		Run: func(r render.Renderer) {
			r.Inventory(moduleFromSrc(t__noopT(), ``))
		},
	},
	{
		// Unused with entities — bullet list with location.
		Name: "unused_populated",
		Run: func(r render.Renderer) {
			r.Unused([]analysis.Entity{
				{Kind: analysis.KindLocal, Name: "dead_code",
					Pos: token.Position{File: "main.tf", Line: 5}},
			})
		},
	},
	{
		// Unused empty — ✅ baseline.
		Name: "unused_empty",
		Run: func(r render.Renderer) {
			r.Unused(nil)
		},
	},
	{
		// CacheInfo with entries — exercises humanBytes formatting
		// (KB/MB humanisation) plus the bullet-list rendering.
		Name: "cache_info_populated",
		Run: func(r render.Renderer) {
			r.CacheInfo("/tmp/tflens-cache", 42, 1024*1024*5+512*1024)
		},
	},
	{
		// CacheAlreadyEmpty — single short line.
		Name: "cache_already_empty",
		Run: func(r render.Renderer) {
			r.CacheAlreadyEmpty("/tmp/tflens-cache")
		},
	},
	{
		// CacheCleared — pluralY's "y" branch (1 entry) + humanBytes.
		Name: "cache_cleared_one",
		Run: func(r render.Renderer) {
			r.CacheCleared(1, 4096, "/tmp/tflens-cache")
		},
	},
	{
		// CacheCleared — pluralY's "ies" branch (multi-entry).
		Name: "cache_cleared_many",
		Run: func(r render.Renderer) {
			r.CacheCleared(7, 1024*1024, "/tmp/tflens-cache")
		},
	},
	{
		// FmtParseErrors with one diag carrying a Subject (file:pos)
		// + one without — pins both formatting branches.
		Name: "fmt_parse_errors",
		Run: func(r render.Renderer) {
			r.FmtParseErrors(hcl.Diagnostics{
				{
					Severity: hcl.DiagError,
					Summary:  "Unexpected token",
					Detail:   "Expected `}`, found `=`",
					Subject:  &hcl.Range{Filename: "main.tf", Start: hcl.Pos{Line: 12, Column: 5}},
				},
				{
					Severity: hcl.DiagError,
					Summary:  "Generic error",
					Detail:   "Something went wrong",
				},
			})
		},
	},
}

// ---- branch-coverage micro-cases ----
//
// These pin edge branches that the larger fixtures don't naturally
// hit (single-issue plural form, empty sensitive values, location
// with no file path, etc.). Kept here so the same -update flag
// regenerates everything in one pass.

var markdownMicroCases = []markdownCase{
	{
		// Validate with exactly one error → "1 issue found" hits
		// plural's n==1 branch (returns "" for "issues" → "issue").
		Name: "validate_single_error",
		Run: func(r render.Renderer) {
			r.Validate(
				[]analysis.ValidationError{{
					EntityID: "resource.aws_vpc.main",
					Ref:      "var.typo",
					Pos:      token.Position{File: "main.tf", Line: 12},
				}},
				nil, nil,
			)
		},
	},
	{
		// Validate with a position carrying line but no file →
		// hits locationCode's File=="" && Line!=0 branch.
		Name: "validate_no_file_position",
		Run: func(r render.Renderer) {
			r.Validate(
				[]analysis.ValidationError{{
					EntityID: "resource.aws_vpc.main",
					Ref:      "var.typo",
					Pos:      token.Position{Line: 12}, // no File
				}},
				nil, nil,
			)
		},
	},
	{
		// Statediff sensitive change with empty old and new values →
		// displayValue's empty-branch returns "(absent)" twice.
		Name: "statediff_sensitive_empty_values",
		Run: func(r render.Renderer) {
			r.Statediff(&statediff.Result{
				BaseRef: "main", Path: "./infra",
				SensitiveChanges: []statediff.SensitiveChange{{
					Kind: "variable", Name: "absent_default",
					Module: "module.api",
				}},
			})
		},
	},
}

// TestRendererMarkdownMicroCases runs the small branch-coverage
// fixtures through the same harness as TestRendererMarkdownCases.
// Split out from the main table because they're noise from a
// "what does the markdown look like" perspective — they exist to
// pin defensive branches, not to demonstrate output shape.
func TestRendererMarkdownMicroCases(t *testing.T) {
	for _, tc := range markdownMicroCases {
		t.Run(tc.Name, func(t *testing.T) {
			var b bytes.Buffer
			r := markdownRenderer(&b)
			tc.Run(r)
			checkMarkdownGolden(t, tc.Name, b.Bytes())
		})
	}
}

// t__noopT returns a *testing.T-shaped value that moduleFromSrc can
// take. moduleFromSrc requires a *testing.T to call Helper() and
// Fatalf(); when used inside the markdown table cases (which run
// later, inside the per-case t.Run), passing the inner-t around is
// awkward — the cases are static. We synthesise a no-op via a
// throwaway test instance: moduleFromSrc never fails for the inputs
// we feed it (well-formed HCL), so the *testing.T is only used for
// the success path's no-op Helper().
//
// If the input HCL ever fails to parse, the panic from a nil-method
// call will make the failure obvious enough — these are tightly-
// scoped fixtures, not production inputs.
func t__noopT() *testing.T {
	return &testing.T{}
}

// checkMarkdownGolden compares got against
// testdata/markdown/<name>.golden.md. Reuses the shared `-update`
// flag so `go test ./pkg/render/... -run TestRendererMarkdownCases
// -update` regenerates every golden in one pass.
//
// .md extension (vs .golden) so editors syntax-highlight the goldens
// as markdown — useful when visually reviewing what a regenerate
// changed.
func checkMarkdownGolden(t *testing.T, name string, got []byte) {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	path := filepath.Join(filepath.Dir(file), "testdata", "markdown", name+".golden.md")
	if *updateGoldens {
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatalf("write golden %s: %v", path, err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read golden %s: %v\n(rerun with -update to create it)", path, err)
	}
	want = bytes.ReplaceAll(want, []byte("\r\n"), []byte("\n"))
	if !bytes.Equal(got, want) {
		t.Errorf("output mismatch for markdown/%s.golden.md\n--- want ---\n%s\n--- got ---\n%s",
			name, want, got)
	}
}
