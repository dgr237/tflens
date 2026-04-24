package render_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/render"
	"github.com/dgr237/tflens/pkg/statediff"
)

// TestWriteStatediffNil confirms nil-safety — writes nothing.
func TestWriteStatediffNil(t *testing.T) {
	var buf bytes.Buffer
	render.WriteStatediff(&buf, nil)
	if buf.Len() != 0 {
		t.Errorf("nil result should write nothing, got %q", buf.String())
	}
}

// TestWriteStatediffEmptyResultEmitsBaselineMessage covers the "no
// changes detected" path that runs when nothing was flagged AND no
// state orphans exist.
func TestWriteStatediffEmptyResultEmitsBaselineMessage(t *testing.T) {
	var buf bytes.Buffer
	render.WriteStatediff(&buf, &statediff.Result{BaseRef: "main"})
	got := buf.String()
	want := "No resource identity or sensitive-local changes detected vs main.\n"
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// TestWriteStatediffAddedAndRemovedResources covers the most common
// case — formatting + ordering of adds/removes under a single
// "Resource identity changes vs <ref>:" heading.
func TestWriteStatediffAddedAndRemovedResources(t *testing.T) {
	var buf bytes.Buffer
	render.WriteStatediff(&buf, &statediff.Result{
		BaseRef: "main",
		AddedResources: []statediff.ResourceRef{
			{Type: "aws_vpc", Name: "main", Mode: "managed"},
			{Module: "module.app", Type: "aws_instance", Name: "web", Mode: "managed"},
		},
		RemovedResources: []statediff.ResourceRef{
			{Type: "aws_subnet", Name: "old", Mode: "managed"},
		},
	})
	got := buf.String()
	want := "" +
		"Resource identity changes vs main:\n" +
		"  + aws_vpc.main (managed)\n" +
		"  + module.app.aws_instance.web (managed)\n" +
		"  - aws_subnet.old (managed)\n"
	if got != want {
		t.Errorf("got:\n%q\n\nwant:\n%q", got, want)
	}
}

// TestWriteStatediffRenamesUnderOwnHeading: rename pairs go in their
// own labelled section after the identity-changes section.
func TestWriteStatediffRenamesUnderOwnHeading(t *testing.T) {
	var buf bytes.Buffer
	render.WriteStatediff(&buf, &statediff.Result{
		BaseRef: "main",
		RenamedResources: []statediff.RenamePair{
			{Module: "module.vpc", From: "resource.aws_subnet.old", To: "resource.aws_subnet.new"},
		},
	})
	got := buf.String()
	if !strings.Contains(got, "Renames (moved block handled") {
		t.Errorf("missing rename heading; got:\n%s", got)
	}
	if !strings.Contains(got, "module.vpc.aws_subnet.old → module.vpc.aws_subnet.new") {
		t.Errorf("missing rename arrow line; got:\n%s", got)
	}
}

// TestWriteStatediffSensitiveChangeNoStateInstances: a sensitive
// change without a loaded state file shows old/new + affected
// resources but no per-instance lines.
func TestWriteStatediffSensitiveChangeNoStateInstances(t *testing.T) {
	var buf bytes.Buffer
	render.WriteStatediff(&buf, &statediff.Result{
		BaseRef: "main",
		SensitiveChanges: []statediff.SensitiveChange{
			{
				Kind: "local", Name: "enabled",
				OldValue: "1", NewValue: "0",
				AffectedResources: []statediff.AffectedResource{
					{Type: "aws_vpc", Name: "main", MetaArg: "count"},
				},
			},
		},
	})
	got := buf.String()
	for _, want := range []string{
		"Value changes that may alter count/for_each expansion:",
		"  - local.enabled",
		"      old: 1",
		"      new: 0",
		"    Affected: aws_vpc.main (count)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output:\n%s", want, got)
		}
	}
	// No state instances → no bullet points
	if strings.Contains(got, "state instance:") {
		t.Errorf("should not render state instance bullets when none present:\n%s", got)
	}
}

// TestWriteStatediffSensitiveChangeWithStateInstances confirms
// per-instance bullets appear when affected resource has state
// addresses attached.
func TestWriteStatediffSensitiveChangeWithStateInstances(t *testing.T) {
	var buf bytes.Buffer
	render.WriteStatediff(&buf, &statediff.Result{
		BaseRef: "main",
		SensitiveChanges: []statediff.SensitiveChange{
			{
				Kind: "local", Name: "regions",
				OldValue: `["us-east-1", "us-west-2"]`,
				NewValue: `["us-east-1"]`,
				AffectedResources: []statediff.AffectedResource{
					{Type: "aws_vpc", Name: "main", MetaArg: "for_each",
						StateInstances: []string{
							`aws_vpc.main["us-east-1"]`,
							`aws_vpc.main["us-west-2"]`,
						}},
				},
			},
		},
	})
	got := buf.String()
	for _, want := range []string{
		`      • state instance: aws_vpc.main["us-east-1"]`,
		`      • state instance: aws_vpc.main["us-west-2"]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output:\n%s", want, got)
		}
	}
}

// TestWriteStatediffOrAbsent confirms an empty value renders as
// "(absent)" — distinguishes "default removed" from "default = ''".
func TestWriteStatediffOrAbsent(t *testing.T) {
	var buf bytes.Buffer
	render.WriteStatediff(&buf, &statediff.Result{
		BaseRef: "main",
		SensitiveChanges: []statediff.SensitiveChange{
			{Kind: "variable", Name: "n", OldValue: "3", NewValue: ""},
		},
	})
	got := buf.String()
	if !strings.Contains(got, "      new: (absent)") {
		t.Errorf("empty NewValue should render as (absent); got:\n%s", got)
	}
}

// TestWriteStatediffStateOrphans: a result with only orphans + no
// other findings still emits the orphan section but skips the
// "no changes detected" baseline (because there ARE orphans).
func TestWriteStatediffStateOrphans(t *testing.T) {
	var buf bytes.Buffer
	render.WriteStatediff(&buf, &statediff.Result{
		BaseRef:      "main",
		StateOrphans: []string{"aws_eip.unused", `aws_subnet.old["a"]`},
	})
	got := buf.String()
	for _, want := range []string{
		"State drift — addresses in state but not declared in the new tree:",
		"  ? aws_eip.unused",
		`  ? aws_subnet.old["a"]`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in output:\n%s", want, got)
		}
	}
	if strings.Contains(got, "No resource identity") {
		t.Errorf("should not emit baseline message when orphans present:\n%s", got)
	}
}

// TestWriteStatediffSectionsSeparatedByBlankLine confirms inter-section
// spacing — distinct sections always have a blank line between them.
func TestWriteStatediffSectionsSeparatedByBlankLine(t *testing.T) {
	var buf bytes.Buffer
	render.WriteStatediff(&buf, &statediff.Result{
		BaseRef:        "main",
		AddedResources: []statediff.ResourceRef{{Type: "aws_vpc", Name: "main", Mode: "managed"}},
		RenamedResources: []statediff.RenamePair{
			{From: "resource.aws_subnet.old", To: "resource.aws_subnet.new"},
		},
	})
	got := buf.String()
	// Should contain an empty line between the two sections — i.e.
	// "\n\n" appears somewhere (after the last add line, before the
	// rename heading).
	if !strings.Contains(got, "\n\nRenames") {
		t.Errorf("expected blank line before Renames section; got:\n%s", got)
	}
}
