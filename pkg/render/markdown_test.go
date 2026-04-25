package render_test

import (
	"bytes"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/statediff"
	"github.com/dgr237/tflens/pkg/token"
)

// markdownCase runs one MarkdownRenderer scenario through a thunk and
// matches the captured bytes against testdata/markdown/<Name>.golden.md.
// Each test passes its inputs inline via the Run thunk so cases can
// exercise different renderer methods (Diff/Whatif/Statediff/Validate)
// from one harness — markdown's value is in PR-comment shape, so we
// pin every primary surface.
type markdownCase struct {
	Name string
	Run  func(r interface {
		Diff(baseRef, path string, results []diff.PairResult, rootChanges []diff.Change)
		Whatif(baseRef, path string, calls []diff.WhatifResult)
		Statediff(result *statediff.Result)
		Validate(refErrs, crossErrs []analysis.ValidationError, typeErrs []analysis.TypeCheckError)
	})
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
		Run: func(r interface {
			Diff(string, string, []diff.PairResult, []diff.Change)
			Whatif(string, string, []diff.WhatifResult)
			Statediff(*statediff.Result)
			Validate([]analysis.ValidationError, []analysis.ValidationError, []analysis.TypeCheckError)
		}) {
			r.Diff("main", "./infra", nil, nil)
		},
	},
	{
		// Diff with mixed-kind root + module sections — exercises
		// severity badges, summary totals, the open=open attribute
		// on Breaking-containing details, and the fix-hint emission.
		Name: "diff_mixed_changes",
		Run: func(r interface {
			Diff(string, string, []diff.PairResult, []diff.Change)
			Whatif(string, string, []diff.WhatifResult)
			Statediff(*statediff.Result)
			Validate([]analysis.ValidationError, []analysis.ValidationError, []analysis.TypeCheckError)
		}) {
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
		Run: func(r interface {
			Diff(string, string, []diff.PairResult, []diff.Change)
			Whatif(string, string, []diff.WhatifResult)
			Statediff(*statediff.Result)
			Validate([]analysis.ValidationError, []analysis.ValidationError, []analysis.TypeCheckError)
		}) {
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
		Run: func(r interface {
			Diff(string, string, []diff.PairResult, []diff.Change)
			Whatif(string, string, []diff.WhatifResult)
			Statediff(*statediff.Result)
			Validate([]analysis.ValidationError, []analysis.ValidationError, []analysis.TypeCheckError)
		}) {
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
		Run: func(r interface {
			Diff(string, string, []diff.PairResult, []diff.Change)
			Whatif(string, string, []diff.WhatifResult)
			Statediff(*statediff.Result)
			Validate([]analysis.ValidationError, []analysis.ValidationError, []analysis.TypeCheckError)
		}) {
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
		Run: func(r interface {
			Diff(string, string, []diff.PairResult, []diff.Change)
			Whatif(string, string, []diff.WhatifResult)
			Statediff(*statediff.Result)
			Validate([]analysis.ValidationError, []analysis.ValidationError, []analysis.TypeCheckError)
		}) {
			r.Statediff(&statediff.Result{BaseRef: "main", Path: "./infra"})
		},
	},
	{
		// Validate with a mix of error kinds → all three subsections
		// (undefined refs, cross-module, type errors) plus the
		// total-count summary line.
		Name: "validate_mixed_errors",
		Run: func(r interface {
			Diff(string, string, []diff.PairResult, []diff.Change)
			Whatif(string, string, []diff.WhatifResult)
			Statediff(*statediff.Result)
			Validate([]analysis.ValidationError, []analysis.ValidationError, []analysis.TypeCheckError)
		}) {
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
		Run: func(r interface {
			Diff(string, string, []diff.PairResult, []diff.Change)
			Whatif(string, string, []diff.WhatifResult)
			Statediff(*statediff.Result)
			Validate([]analysis.ValidationError, []analysis.ValidationError, []analysis.TypeCheckError)
		}) {
			r.Validate(nil, nil, nil)
		},
	},
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
