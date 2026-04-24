package render_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/render"
)

func TestJSONSummaryAddBucketsByKind(t *testing.T) {
	var s render.JSONSummary
	for _, k := range []diff.ChangeKind{diff.Breaking, diff.Breaking, diff.NonBreaking, diff.Informational} {
		s.Add(diff.Change{Kind: k})
	}
	if s.Breaking != 2 || s.NonBreaking != 1 || s.Informational != 1 {
		t.Errorf("got %+v, want {2,1,1}", s)
	}
}

func TestBuildJSONDiffEmptyResultsHasZeroSummary(t *testing.T) {
	got := render.BuildJSONDiff("main", ".", nil, nil)
	if got.BaseRef != "main" || got.Path != "." {
		t.Errorf("envelope fields: %+v", got)
	}
	if got.Summary != (render.JSONSummary{}) {
		t.Errorf("summary should be zero, got %+v", got.Summary)
	}
	if got.RootChanges != nil {
		t.Errorf("RootChanges should be nil for empty input, got %v", got.RootChanges)
	}
}

func TestBuildJSONDiffRootChangesContributeToSummary(t *testing.T) {
	got := render.BuildJSONDiff("main", ".", nil, []diff.Change{
		{Kind: diff.Breaking, Subject: "variable.x", Detail: "removed"},
		{Kind: diff.NonBreaking, Subject: "variable.y", Detail: "added"},
		{Kind: diff.Informational, Subject: "out.docs", Detail: "doc"},
	})
	if got.Summary.Breaking != 1 || got.Summary.NonBreaking != 1 || got.Summary.Informational != 1 {
		t.Errorf("root counts wrong: %+v", got.Summary)
	}
	if len(got.RootChanges) != 3 {
		t.Errorf("RootChanges len = %d, want 3", len(got.RootChanges))
	}
}

func TestBuildJSONDiffSkipsUninterestingPairs(t *testing.T) {
	// A "changed" pair with no content + no attr move is uninteresting.
	uninteresting := diff.PairResult{Pair: loader.ModuleCallPair{
		Key: "vpc", Status: loader.StatusChanged,
		OldSource: "x", NewSource: "x",
	}}
	interesting := diff.PairResult{
		Pair: loader.ModuleCallPair{
			Key: "sg", Status: loader.StatusChanged,
			OldSource: "x", NewSource: "y", // attr move → interesting
		},
	}
	got := render.BuildJSONDiff("main", ".", []diff.PairResult{uninteresting, interesting}, nil)
	if len(got.Modules) != 1 || got.Modules[0].Name != "sg" {
		t.Errorf("expected only 'sg' to survive filter, got %+v", got.Modules)
	}
}

func TestBuildJSONDiffPerModuleAndOverallSummary(t *testing.T) {
	got := render.BuildJSONDiff("main", ".", []diff.PairResult{{
		Pair: loader.ModuleCallPair{
			Key: "vpc", Status: loader.StatusChanged,
			OldSource: "ns/vpc/aws", NewSource: "ns/vpc/aws",
			OldVersion: "1.0.0", NewVersion: "2.0.0",
		},
		Changes: []diff.Change{
			{Kind: diff.Breaking, Subject: "variable.cidr", Detail: "removed"},
			{Kind: diff.NonBreaking, Subject: "variable.tags", Detail: "added optional"},
		},
	}}, []diff.Change{
		{Kind: diff.Informational, Subject: "out.docs", Detail: "doc"},
	})
	if len(got.Modules) != 1 {
		t.Fatalf("Modules len = %d, want 1", len(got.Modules))
	}
	m := got.Modules[0]
	// Per-module summary.
	if m.Summary.Breaking != 1 || m.Summary.NonBreaking != 1 || m.Summary.Informational != 0 {
		t.Errorf("per-module summary: %+v", m.Summary)
	}
	// Overall summary aggregates root + per-module.
	if got.Summary.Breaking != 1 || got.Summary.NonBreaking != 1 || got.Summary.Informational != 1 {
		t.Errorf("overall summary: %+v, want {1,1,1}", got.Summary)
	}
	// Source/version fields populated.
	if m.OldVersion != "1.0.0" || m.NewVersion != "2.0.0" {
		t.Errorf("versions on per-module entry: %+v", m)
	}
}

func TestBuildJSONDiffMarshalsToStableShape(t *testing.T) {
	out := render.BuildJSONDiff("main", ".", nil, []diff.Change{
		{Kind: diff.Breaking, Subject: "var.x"},
	})
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	for _, want := range []string{
		`"base_ref": "main"`,
		`"path": "."`,
		`"root_changes":`,
		`"summary":`,
		`"breaking": 1`,
		`"non_breaking": 0`,
		`"informational": 0`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("missing %q in marshalled JSON:\n%s", want, s)
		}
	}
	// `modules` may be null since input is empty; that's fine, but
	// the field must still appear (no omitempty on Modules).
	if !strings.Contains(s, `"modules":`) {
		t.Errorf("`modules` field should always be present:\n%s", s)
	}
}

func TestBuildJSONWhatifEmptyCallsHasZeroSummary(t *testing.T) {
	got := render.BuildJSONWhatif("main", ".", nil)
	if got.BaseRef != "main" || got.Path != "." {
		t.Errorf("envelope fields: %+v", got)
	}
	if got.Summary != (render.JSONWhatifSummary{}) {
		t.Errorf("summary should be zero, got %+v", got.Summary)
	}
}

func TestBuildJSONWhatifAggregatesDirectImpactAndAPIChanges(t *testing.T) {
	got := render.BuildJSONWhatif("main", ".", []diff.WhatifResult{{
		Pair: loader.ModuleCallPair{Key: "child", Status: loader.StatusChanged},
		DirectImpact: []analysis.ValidationError{
			{EntityID: "module.child", Msg: "missing required input \"x\""},
			{EntityID: "module.child", Msg: "passes unknown argument \"y\""},
		},
		APIChanges: []diff.Change{
			{Kind: diff.Breaking, Subject: "variable.y", Detail: "removed"},
			{Kind: diff.NonBreaking, Subject: "variable.x", Detail: "added required"},
			{Kind: diff.Informational, Subject: "out.docs", Detail: "doc"},
		},
	}})
	if got.Summary.DirectImpact != 2 {
		t.Errorf("DirectImpact = %d, want 2", got.Summary.DirectImpact)
	}
	if got.Summary.Breaking != 1 || got.Summary.NonBreaking != 1 || got.Summary.Informational != 1 {
		t.Errorf("Summary counts: %+v", got.Summary)
	}
	if len(got.Calls) != 1 {
		t.Fatalf("Calls len = %d, want 1", len(got.Calls))
	}
	c := got.Calls[0]
	if c.Name != "child" || c.Status != "changed" {
		t.Errorf("Call shape: %+v", c)
	}
	if len(c.DirectImpact) != 2 || len(c.APIChanges) != 3 {
		t.Errorf("DirectImpact/APIChanges len = %d/%d", len(c.DirectImpact), len(c.APIChanges))
	}
}

func TestJSONWhatifSummaryEmbedsCleanly(t *testing.T) {
	// The wire format must keep all four counter fields at the same
	// level — direct_impact, breaking, non_breaking, informational —
	// not nested inside a "summary" sub-object inside summary.
	s := render.JSONWhatifSummary{
		DirectImpact: 7,
		JSONSummary: render.JSONSummary{
			Breaking: 2, NonBreaking: 1, Informational: 5,
		},
	}
	b, _ := json.Marshal(s)
	got := string(b)
	for _, want := range []string{
		`"direct_impact":7`,
		`"breaking":2`,
		`"non_breaking":1`,
		`"informational":5`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in flat JSON %s", want, got)
		}
	}
	// No nested "JSONSummary" key.
	if strings.Contains(got, "JSONSummary") {
		t.Errorf("embed should be promoted, not appear as nested object: %s", got)
	}
}
