package render_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
)

// ---- Diff ----

func TestRendererDiffEmptyEmitsBaseline(t *testing.T) {
	var buf bytes.Buffer
	consoleRenderer(&buf).Diff("main", ".", nil, nil)
	if got := buf.String(); got != "No changes detected vs main.\n" {
		t.Errorf("got %q", got)
	}
}

func TestRendererDiffAllUninterestingEmitsBaseline(t *testing.T) {
	var buf bytes.Buffer
	consoleRenderer(&buf).Diff("main", ".", []diff.PairResult{
		{Pair: loader.ModuleCallPair{Key: "x", Status: loader.StatusChanged, OldSource: "a", NewSource: "a"}},
	}, nil)
	if !strings.Contains(buf.String(), "No changes detected") {
		t.Errorf("uninteresting pair should yield baseline; got:\n%s", buf.String())
	}
}

func TestRendererDiffRootPlusModuleSeparatedByBlankLine(t *testing.T) {
	var buf bytes.Buffer
	consoleRenderer(&buf).Diff("main", ".",
		[]diff.PairResult{{
			Pair: loader.ModuleCallPair{
				Key: "vpc", Status: loader.StatusChanged,
				OldSource: "x", NewSource: "y",
			},
		}},
		[]diff.Change{{Kind: diff.Breaking, Subject: "var.x", Detail: "removed"}},
	)
	got := buf.String()
	if !strings.Contains(got, "Root module:") {
		t.Errorf("missing Root module heading:\n%s", got)
	}
	if !strings.Contains(got, "Module \"vpc\":") {
		t.Errorf("missing Module heading:\n%s", got)
	}
	if !strings.Contains(got, "\n\nModule \"vpc\":") {
		t.Errorf("expected blank line before Module section; got:\n%s", got)
	}
}

// TestRendererDiffRootChangesUseCanonicalIndents pins down the root-
// module section's indentation contract — the cmd layer relies on
// the "  Breaking (N):" / "    subject: detail" shape for visual
// consistency with the per-module sections.
func TestRendererDiffRootChangesUseCanonicalIndents(t *testing.T) {
	var buf bytes.Buffer
	consoleRenderer(&buf).Diff("main", ".", nil, []diff.Change{
		{Kind: diff.Breaking, Subject: "variable.test", Detail: "required variable added"},
	})
	want := "" +
		"Root module:\n" +
		"  Breaking (1):\n" +
		"    variable.test: required variable added\n"
	if got := buf.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---- per-pair text ----

func TestRendererDiffPairAdded(t *testing.T) {
	var buf bytes.Buffer
	consoleRenderer(&buf).Diff("main", ".", []diff.PairResult{{
		Pair: loader.ModuleCallPair{
			Key: "vpc", Status: loader.StatusAdded,
			NewSource: "ns/vpc/aws", NewVersion: "1.0.0",
		},
	}}, nil)
	if !strings.Contains(buf.String(), `Module "vpc": ADDED (source=ns/vpc/aws, version=1.0.0)`) {
		t.Errorf("missing ADDED line; got:\n%s", buf.String())
	}
}

func TestRendererDiffPairAddedWithoutVersion(t *testing.T) {
	var buf bytes.Buffer
	consoleRenderer(&buf).Diff("main", ".", []diff.PairResult{{
		Pair: loader.ModuleCallPair{
			Key: "vpc", Status: loader.StatusAdded,
			NewSource: "./modules/vpc",
		},
	}}, nil)
	got := buf.String()
	if !strings.Contains(got, "ADDED (source=./modules/vpc)") {
		t.Errorf("local source without version should omit version=; got %q", got)
	}
	if strings.Contains(got, "version=") {
		t.Errorf("version= should not appear when NewVersion is empty; got %q", got)
	}
}

func TestRendererDiffPairRemoved(t *testing.T) {
	var buf bytes.Buffer
	consoleRenderer(&buf).Diff("main", ".", []diff.PairResult{{
		Pair: loader.ModuleCallPair{
			Key: "vpc", Status: loader.StatusRemoved,
			OldSource: "ns/vpc/aws", OldVersion: "1.0.0",
		},
	}}, nil)
	if !strings.Contains(buf.String(), `Module "vpc": REMOVED (was source=ns/vpc/aws, version=1.0.0)`) {
		t.Errorf("missing REMOVED line; got:\n%s", buf.String())
	}
}

func TestRendererDiffPairChangedSourceAndVersion(t *testing.T) {
	var buf bytes.Buffer
	consoleRenderer(&buf).Diff("main", ".", []diff.PairResult{{
		Pair: loader.ModuleCallPair{
			Key: "vpc", Status: loader.StatusChanged,
			OldSource: "ns/vpc/aws", NewSource: "ns/vpc-v2/aws",
			OldVersion: "1.0.0", NewVersion: "2.0.0",
		},
		Changes: []diff.Change{{Kind: diff.Breaking, Subject: "var.x", Detail: "removed"}},
	}}, nil)
	got := buf.String()
	for _, want := range []string{
		`Module "vpc":`,
		`source ns/vpc/aws → ns/vpc-v2/aws`,
		`version "1.0.0" → "2.0.0"`,
		`  Breaking (1):`,
		`    var.x: removed`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// TestRendererDiffPairChangedContentOnly: source + version unchanged
// but Changes is non-empty (e.g. content drift on a local source).
// The heading ends with "(content changed)" instead of attr arrows.
func TestRendererDiffPairChangedContentOnly(t *testing.T) {
	var buf bytes.Buffer
	consoleRenderer(&buf).Diff("main", ".", []diff.PairResult{{
		Pair: loader.ModuleCallPair{
			Key: "vpc", Status: loader.StatusChanged,
			OldSource: "x", NewSource: "x",
		},
		Changes: []diff.Change{{Kind: diff.Breaking, Subject: "var.x", Detail: "removed"}},
	}}, nil)
	if !strings.Contains(buf.String(), "(content changed)") {
		t.Errorf("expected (content changed) marker; got:\n%s", buf.String())
	}
}

// TestRendererDiffPairChangedNoAPIChanges: status=changed, source moved
// (so the attr line shows arrows), but Changes is empty — emits
// "(no API changes)" line.
func TestRendererDiffPairChangedNoAPIChanges(t *testing.T) {
	var buf bytes.Buffer
	consoleRenderer(&buf).Diff("main", ".", []diff.PairResult{{
		Pair: loader.ModuleCallPair{
			Key: "vpc", Status: loader.StatusChanged,
			OldSource: "x", NewSource: "y",
		},
	}}, nil)
	if !strings.Contains(buf.String(), "(no API changes)") {
		t.Errorf("expected (no API changes); got:\n%s", buf.String())
	}
}

// ---- Whatif ----

func TestRendererWhatifEmptyEmitsBaseline(t *testing.T) {
	var buf bytes.Buffer
	consoleRenderer(&buf).Whatif("main", "./modules/x", nil)
	if got := buf.String(); got != "No upgraded module calls to simulate (path vs main).\n" {
		t.Errorf("got %q", got)
	}
}

func TestRendererWhatifRemovedShortLine(t *testing.T) {
	var buf bytes.Buffer
	consoleRenderer(&buf).Whatif("main", ".", []diff.WhatifResult{{
		Pair: loader.ModuleCallPair{
			Key: "vpc", Status: loader.StatusRemoved,
			OldSource: "ns/vpc/aws", OldVersion: "1.0.0",
		},
	}})
	if !strings.Contains(buf.String(), "module.vpc: REMOVED (was source=ns/vpc/aws, version=\"1.0.0\")") {
		t.Errorf("missing REMOVED line; got:\n%s", buf.String())
	}
}

func TestRendererWhatifNoDirectImpact(t *testing.T) {
	var buf bytes.Buffer
	consoleRenderer(&buf).Whatif("main", ".", []diff.WhatifResult{{
		Pair: loader.ModuleCallPair{Key: "vpc", Status: loader.StatusChanged},
	}})
	got := buf.String()
	for _, want := range []string{
		"Direct impact on module.vpc in . (0 issue(s)):",
		"(none — callers at base are compatible with the new child)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRendererWhatifWithDirectImpactAndAPIChanges(t *testing.T) {
	var buf bytes.Buffer
	consoleRenderer(&buf).Whatif("main", ".", []diff.WhatifResult{{
		Pair: loader.ModuleCallPair{Key: "vpc", Status: loader.StatusChanged},
		DirectImpact: []analysis.ValidationError{
			{EntityID: "module.vpc", Msg: "missing required input \"x\""},
		},
		APIChanges: []diff.Change{
			{Kind: diff.Breaking, Subject: "variable.x", Detail: "removed"},
		},
	}})
	got := buf.String()
	for _, want := range []string{
		"Direct impact on module.vpc in . (1 issue(s)):",
		"  API changes for module.vpc:",
		"    Breaking (1):",
		"      variable.x: removed",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

func TestRendererWhatifMultipleCallsSeparatedByBlankLine(t *testing.T) {
	var buf bytes.Buffer
	consoleRenderer(&buf).Whatif("main", ".", []diff.WhatifResult{
		{Pair: loader.ModuleCallPair{Key: "a", Status: loader.StatusChanged}},
		{Pair: loader.ModuleCallPair{Key: "b", Status: loader.StatusChanged}},
	})
	got := buf.String()
	if !strings.Contains(got, "Direct impact on module.a") {
		t.Errorf("missing first call:\n%s", got)
	}
	if !strings.Contains(got, "Direct impact on module.b") {
		t.Errorf("missing second call:\n%s", got)
	}
	if !strings.Contains(got, "\n\nDirect impact on module.b") {
		t.Errorf("expected blank line before second call; got:\n%s", got)
	}
}
