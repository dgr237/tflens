package render_test

import (
	"bytes"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
)

// diffCase describes one Diff text-rendering scenario. The renderer
// is invoked with (BaseRef, Path, Results, RootChanges) and the
// captured bytes are compared against testdata/diff/<Name>.golden.
//
// Adding a case: append the struct entry, then `go test
// ./pkg/render/... -run TestRendererDiffCases -update` to write the
// golden file. Review the diff before committing.
type diffCase struct {
	Name        string
	BaseRef     string
	Path        string
	Results     []diff.PairResult
	RootChanges []diff.Change
}

func TestRendererDiffCases(t *testing.T) {
	for _, tc := range diffCases {
		t.Run(tc.Name, func(t *testing.T) {
			var b bytes.Buffer
			consoleRenderer(&b).Diff(tc.BaseRef, tc.Path, tc.Results, tc.RootChanges)
			checkGolden(t, "diff", tc.Name, b.Bytes())
		})
	}
}

var diffCases = []diffCase{
	{
		// nil/nil produces the "no changes" baseline.
		Name:    "empty",
		BaseRef: "main",
		Path:    ".",
	},
	{
		// A "changed" pair with no content + no attr move is filtered
		// as uninteresting; output is the same baseline.
		Name:    "all_uninteresting",
		BaseRef: "main",
		Path:    ".",
		Results: []diff.PairResult{{
			Pair: loader.ModuleCallPair{Key: "x", Status: loader.StatusChanged, OldSource: "a", NewSource: "a"},
		}},
	},
	{
		// Root + one Module section. The renderer emits the Root
		// header first then a blank line then the Module header.
		Name:    "root_plus_module",
		BaseRef: "main",
		Path:    ".",
		Results: []diff.PairResult{{
			Pair: loader.ModuleCallPair{
				Key: "vpc", Status: loader.StatusChanged,
				OldSource: "x", NewSource: "y",
			},
		}},
		RootChanges: []diff.Change{{Kind: diff.Breaking, Subject: "var.x", Detail: "removed"}},
	},
	{
		// Root-only changes use canonical "  " heading / "    " line
		// indents — pin for visual consistency with module sections.
		Name:    "root_changes_canonical_indents",
		BaseRef: "main",
		Path:    ".",
		RootChanges: []diff.Change{
			{Kind: diff.Breaking, Subject: "variable.test", Detail: "required variable added"},
		},
	},
	{
		// New registry call: ADDED line carries (source=..., version=...).
		Name:    "pair_added_with_version",
		BaseRef: "main",
		Path:    ".",
		Results: []diff.PairResult{{
			Pair: loader.ModuleCallPair{
				Key: "vpc", Status: loader.StatusAdded,
				NewSource: "ns/vpc/aws", NewVersion: "1.0.0",
			},
		}},
	},
	{
		// Local-source ADDED: no version means no "version=" suffix.
		Name:    "pair_added_local_source_no_version",
		BaseRef: "main",
		Path:    ".",
		Results: []diff.PairResult{{
			Pair: loader.ModuleCallPair{
				Key: "vpc", Status: loader.StatusAdded,
				NewSource: "./modules/vpc",
			},
		}},
	},
	{
		// REMOVED line carries (was source=..., version=...).
		Name:    "pair_removed",
		BaseRef: "main",
		Path:    ".",
		Results: []diff.PairResult{{
			Pair: loader.ModuleCallPair{
				Key: "vpc", Status: loader.StatusRemoved,
				OldSource: "ns/vpc/aws", OldVersion: "1.0.0",
			},
		}},
	},
	{
		// Source + version both moved AND there are content changes —
		// pin the full multi-line shape. The Hint also exercises
		// writeChange's "  hint: ..." emission line.
		Name:    "pair_changed_source_and_version",
		BaseRef: "main",
		Path:    ".",
		Results: []diff.PairResult{{
			Pair: loader.ModuleCallPair{
				Key: "vpc", Status: loader.StatusChanged,
				OldSource: "ns/vpc/aws", NewSource: "ns/vpc-v2/aws",
				OldVersion: "1.0.0", NewVersion: "2.0.0",
			},
			Changes: []diff.Change{{
				Kind:    diff.Breaking,
				Subject: "var.x",
				Detail:  "removed",
				Hint:    "callers passing this variable will fail",
			}},
		}},
	},
	{
		// Mixed change kinds in one pair — exercises every arm of
		// bucketByKind (Breaking / NonBreaking / Informational) and
		// the section ordering between them.
		Name:    "pair_mixed_change_kinds",
		BaseRef: "main",
		Path:    ".",
		Results: []diff.PairResult{{
			Pair: loader.ModuleCallPair{
				Key: "vpc", Status: loader.StatusChanged,
				OldSource: "x", NewSource: "x",
			},
			Changes: []diff.Change{
				{Kind: diff.Breaking, Subject: "var.required", Detail: "removed"},
				{Kind: diff.NonBreaking, Subject: "var.tags", Detail: "added optional"},
				{Kind: diff.Informational, Subject: "out.docs", Detail: "description updated"},
			},
		}},
	},
	{
		// Source + version unchanged but Changes is non-empty — the
		// heading reads "(content changed)" instead of attr arrows.
		Name:    "pair_changed_content_only",
		BaseRef: "main",
		Path:    ".",
		Results: []diff.PairResult{{
			Pair: loader.ModuleCallPair{
				Key: "vpc", Status: loader.StatusChanged,
				OldSource: "x", NewSource: "x",
			},
			Changes: []diff.Change{{Kind: diff.Breaking, Subject: "var.x", Detail: "removed"}},
		}},
	},
	{
		// Source moved but Changes is empty — emits "(no API changes)".
		Name:    "pair_changed_no_api_changes",
		BaseRef: "main",
		Path:    ".",
		Results: []diff.PairResult{{
			Pair: loader.ModuleCallPair{
				Key: "vpc", Status: loader.StatusChanged,
				OldSource: "x", NewSource: "y",
			},
		}},
	},
}

// whatifCase mirrors diffCase for the Whatif renderer. Goldens live
// under testdata/whatif/<Name>.golden.
type whatifCase struct {
	Name    string
	BaseRef string
	Path    string
	Calls   []diff.WhatifResult
}

func TestRendererWhatifCases(t *testing.T) {
	for _, tc := range whatifCases {
		t.Run(tc.Name, func(t *testing.T) {
			var b bytes.Buffer
			consoleRenderer(&b).Whatif(tc.BaseRef, tc.Path, tc.Calls)
			checkGolden(t, "whatif", tc.Name, b.Bytes())
		})
	}
}

var whatifCases = []whatifCase{
	{
		// nil calls produces the "no upgraded module calls" baseline.
		Name:    "empty",
		BaseRef: "main",
		Path:    "./modules/x",
	},
	{
		// REMOVED call gets a single short line, no Direct impact /
		// API changes section.
		Name:    "removed_short_line",
		BaseRef: "main",
		Path:    ".",
		Calls: []diff.WhatifResult{{
			Pair: loader.ModuleCallPair{
				Key: "vpc", Status: loader.StatusRemoved,
				OldSource: "ns/vpc/aws", OldVersion: "1.0.0",
			},
		}},
	},
	{
		// Changed call with no DirectImpact issues — emits the
		// "(none — callers compatible)" line under "Direct impact".
		Name:    "no_direct_impact",
		BaseRef: "main",
		Path:    ".",
		Calls: []diff.WhatifResult{{
			Pair: loader.ModuleCallPair{Key: "vpc", Status: loader.StatusChanged},
		}},
	},
	{
		// Direct impact AND API changes both present — both sections
		// render with their canonical indents.
		Name:    "direct_impact_and_api_changes",
		BaseRef: "main",
		Path:    ".",
		Calls: []diff.WhatifResult{{
			Pair: loader.ModuleCallPair{Key: "vpc", Status: loader.StatusChanged},
			DirectImpact: []analysis.ValidationError{
				{EntityID: "module.vpc", Msg: "missing required input \"x\""},
			},
			APIChanges: []diff.Change{
				{Kind: diff.Breaking, Subject: "variable.x", Detail: "removed"},
			},
		}},
	},
	{
		// Two changed calls — separated by a blank line.
		Name:    "multiple_calls_blank_separator",
		BaseRef: "main",
		Path:    ".",
		Calls: []diff.WhatifResult{
			{Pair: loader.ModuleCallPair{Key: "a", Status: loader.StatusChanged}},
			{Pair: loader.ModuleCallPair{Key: "b", Status: loader.StatusChanged}},
		},
	},
}
