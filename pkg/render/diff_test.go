package render_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/render"
)

// ---- WriteDiffResults ----

func TestWriteDiffResultsEmptyEmitsBaseline(t *testing.T) {
	var buf bytes.Buffer
	render.WriteDiffResults(&buf, "main", nil, nil)
	got := buf.String()
	if got != "No changes detected vs main.\n" {
		t.Errorf("got %q", got)
	}
}

func TestWriteDiffResultsAllUninterestingEmitsBaseline(t *testing.T) {
	var buf bytes.Buffer
	render.WriteDiffResults(&buf, "main", []diff.PairResult{
		{Pair: loader.ModuleCallPair{Key: "x", Status: loader.StatusChanged, OldSource: "a", NewSource: "a"}},
	}, nil)
	if !strings.Contains(buf.String(), "No changes detected") {
		t.Errorf("uninteresting pair should yield baseline; got:\n%s", buf.String())
	}
}

func TestWriteDiffResultsRootPlusModuleSeparatedByBlankLine(t *testing.T) {
	var buf bytes.Buffer
	render.WriteDiffResults(&buf, "main",
		[]diff.PairResult{{
			Pair: loader.ModuleCallPair{
				Key: "vpc", Status: loader.StatusChanged,
				OldSource: "x", NewSource: "y", // attr move → interesting
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
	// Blank line separator between sections — i.e. \n\n appears
	// somewhere between the two headings.
	if !strings.Contains(got, "\n\nModule \"vpc\":") {
		t.Errorf("expected blank line before Module section; got:\n%s", got)
	}
}

// ---- WriteRootChanges ----

func TestWriteRootChangesUsesCanonicalIndents(t *testing.T) {
	var buf bytes.Buffer
	render.WriteRootChanges(&buf, []diff.Change{
		{Kind: diff.Breaking, Subject: "variable.test", Detail: "required variable added"},
	})
	got := buf.String()
	want := "" +
		"Root module:\n" +
		"  Breaking (1):\n" +
		"    variable.test: required variable added\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// ---- WritePairResult ----

func TestWritePairResultAdded(t *testing.T) {
	var buf bytes.Buffer
	render.WritePairResult(&buf, diff.PairResult{
		Pair: loader.ModuleCallPair{
			Key: "vpc", Status: loader.StatusAdded,
			NewSource: "ns/vpc/aws", NewVersion: "1.0.0",
		},
	})
	got := buf.String()
	want := "Module \"vpc\": ADDED (source=ns/vpc/aws, version=1.0.0)\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWritePairResultAddedWithoutVersion(t *testing.T) {
	var buf bytes.Buffer
	render.WritePairResult(&buf, diff.PairResult{
		Pair: loader.ModuleCallPair{
			Key: "vpc", Status: loader.StatusAdded,
			NewSource: "./modules/vpc",
		},
	})
	got := buf.String()
	if !strings.Contains(got, "ADDED (source=./modules/vpc)") {
		t.Errorf("local source without version should omit version=; got %q", got)
	}
	if strings.Contains(got, "version=") {
		t.Errorf("version= should not appear when NewVersion is empty; got %q", got)
	}
}

func TestWritePairResultRemoved(t *testing.T) {
	var buf bytes.Buffer
	render.WritePairResult(&buf, diff.PairResult{
		Pair: loader.ModuleCallPair{
			Key: "vpc", Status: loader.StatusRemoved,
			OldSource: "ns/vpc/aws", OldVersion: "1.0.0",
		},
	})
	got := buf.String()
	want := "Module \"vpc\": REMOVED (was source=ns/vpc/aws, version=1.0.0)\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWritePairResultChangedSourceAndVersion(t *testing.T) {
	var buf bytes.Buffer
	render.WritePairResult(&buf, diff.PairResult{
		Pair: loader.ModuleCallPair{
			Key: "vpc", Status: loader.StatusChanged,
			OldSource: "ns/vpc/aws", NewSource: "ns/vpc-v2/aws",
			OldVersion: "1.0.0", NewVersion: "2.0.0",
		},
		Changes: []diff.Change{{Kind: diff.Breaking, Subject: "var.x", Detail: "removed"}},
	})
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

// TestWritePairResultChangedContentOnly: source + version unchanged
// but Changes is non-empty (e.g. content drift on a local source).
// The heading ends with "(content changed)" instead of attr arrows.
func TestWritePairResultChangedContentOnly(t *testing.T) {
	var buf bytes.Buffer
	render.WritePairResult(&buf, diff.PairResult{
		Pair: loader.ModuleCallPair{
			Key: "vpc", Status: loader.StatusChanged,
			OldSource: "x", NewSource: "x",
		},
		Changes: []diff.Change{{Kind: diff.Breaking, Subject: "var.x", Detail: "removed"}},
	})
	got := buf.String()
	if !strings.Contains(got, "(content changed)") {
		t.Errorf("expected (content changed) marker; got:\n%s", got)
	}
}

// TestWritePairResultChangedNoAPIChanges: status=changed, source moved
// (so the attr line shows arrows), but Changes is empty — emits
// "(no API changes)" line.
func TestWritePairResultChangedNoAPIChanges(t *testing.T) {
	var buf bytes.Buffer
	render.WritePairResult(&buf, diff.PairResult{
		Pair: loader.ModuleCallPair{
			Key: "vpc", Status: loader.StatusChanged,
			OldSource: "x", NewSource: "y",
		},
	})
	got := buf.String()
	if !strings.Contains(got, "(no API changes)") {
		t.Errorf("expected (no API changes); got:\n%s", got)
	}
}

// ---- WriteWhatifResults / WriteWhatifCall ----

func TestWriteWhatifResultsEmptyEmitsBaseline(t *testing.T) {
	var buf bytes.Buffer
	render.WriteWhatifResults(&buf, "main", "./modules/x", nil)
	got := buf.String()
	if got != "No upgraded module calls to simulate (path vs main).\n" {
		t.Errorf("got %q", got)
	}
}

func TestWriteWhatifCallRemovedShortLine(t *testing.T) {
	var buf bytes.Buffer
	render.WriteWhatifCall(&buf, ".", diff.WhatifResult{
		Pair: loader.ModuleCallPair{
			Key: "vpc", Status: loader.StatusRemoved,
			OldSource: "ns/vpc/aws", OldVersion: "1.0.0",
		},
	})
	got := buf.String()
	want := "module.vpc: REMOVED (was source=ns/vpc/aws, version=\"1.0.0\")\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestWriteWhatifCallNoDirectImpact(t *testing.T) {
	var buf bytes.Buffer
	render.WriteWhatifCall(&buf, ".", diff.WhatifResult{
		Pair: loader.ModuleCallPair{Key: "vpc", Status: loader.StatusChanged},
	})
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

func TestWriteWhatifCallWithDirectImpactAndAPIChanges(t *testing.T) {
	var buf bytes.Buffer
	render.WriteWhatifCall(&buf, ".", diff.WhatifResult{
		Pair: loader.ModuleCallPair{Key: "vpc", Status: loader.StatusChanged},
		DirectImpact: []analysis.ValidationError{
			{EntityID: "module.vpc", Msg: "missing required input \"x\""},
		},
		APIChanges: []diff.Change{
			{Kind: diff.Breaking, Subject: "variable.x", Detail: "removed"},
		},
	})
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

func TestWriteWhatifResultsMultipleCallsSeparatedByBlankLine(t *testing.T) {
	var buf bytes.Buffer
	render.WriteWhatifResults(&buf, "main", ".", []diff.WhatifResult{
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
	// Blank line between the two
	if !strings.Contains(got, "\n\nDirect impact on module.b") {
		t.Errorf("expected blank line before second call; got:\n%s", got)
	}
}
