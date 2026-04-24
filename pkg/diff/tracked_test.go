package diff_test

import (
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
)

// TestDiffTrackedCrossModuleParentChange exercises the cross-module
// resolution path: a marker on a child resource attribute that
// references a parent-supplied variable. The change is entirely on
// the parent side (new optional variable + a now-conditional local
// passed down to the child). Without parent context, the child's
// view shows nothing changed; with TrackedContext, the diff climbs
// through the parent's call argument and surfaces the local + new
// variable as parent-prefixed refs.
func TestDiffTrackedCrossModuleParentChange(t *testing.T) {
	const childOldSrc = `
variable "cluster_version" { type = string }
resource "aws_eks_cluster" "this" {
  name            = "prod"
  cluster_version = var.cluster_version
}
`
	const childNewSrc = `
variable "cluster_version" { type = string }
resource "aws_eks_cluster" "this" {
  name            = "prod"
  cluster_version = var.cluster_version # tflens:track: EKS minor — bump only after add-on compat
}
`
	const parentOldSrc = `
locals { cluster_version = "1.34" }
module "eks" {
  source          = "./modules/eks"
  cluster_version = local.cluster_version
}
`
	const parentNewSrc = `
variable "upgrade" {
  type    = bool
  default = true
}
locals { cluster_version = var.upgrade ? "1.35" : "1.34" }
module "eks" {
  source          = "./modules/eks"
  cluster_version = local.cluster_version
}
`
	oldChild := analyseFromTestdata(t, "old/child/main.tf", childOldSrc)
	newChild := analyseFromTestdata(t, "new/child/main.tf", childNewSrc)
	oldParent := analyseFromTestdata(t, "old/main.tf", parentOldSrc)
	newParent := analyseFromTestdata(t, "new/main.tf", parentNewSrc)

	// Without parent context: the marker addition produces an
	// Informational-only result, since the child's view shows nothing
	// changed (variable still has no default; resource attr text
	// unchanged).
	bare := diff.DiffTracked(oldChild, newChild)
	if len(bare) != 1 || bare[0].Kind != diff.Informational {
		t.Errorf("DiffTracked without parent ctx: want 1 Informational, got %v", bare)
	}

	// With parent context: parent.local.cluster_version differs and
	// parent.variable.upgrade is newly referenced, so the change is
	// promoted to Breaking with both parent-side facts in the detail.
	got := diff.DiffTrackedCtx(oldChild, newChild, diff.TrackedContext{
		OldParent: oldParent, NewParent: newParent, CallName: "eks",
	})
	if len(got) != 1 {
		t.Fatalf("want 1 change, got %d: %v", len(got), got)
	}
	c := got[0]
	if c.Kind != diff.Breaking {
		t.Errorf("Kind = %v, want Breaking", c.Kind)
	}
	wantSubstrings := []string{
		"marker added",
		"parent.local.cluster_version", `"1.34"`, `var.upgrade ? "1.35" : "1.34"`,
		"parent.variable.upgrade", "true",
	}
	for _, s := range wantSubstrings {
		if !strings.Contains(c.Detail, s) {
			t.Errorf("detail missing %q: %s", s, c.Detail)
		}
	}
	if !strings.Contains(c.Hint, "add-on compat") {
		t.Errorf("hint missing 'add-on compat': %s", c.Hint)
	}
}

// TestDiffTrackedCrossModuleNoChangeStaysQuiet confirms cross-module
// resolution doesn't introduce false positives: marker added in the
// child, NOTHING changed in the parent → still Informational only.
func TestDiffTrackedCrossModuleNoChangeStaysQuiet(t *testing.T) {
	const childOldSrc = `
variable "cluster_version" { type = string }
resource "aws_eks_cluster" "this" {
  cluster_version = var.cluster_version
}
`
	const childNewSrc = `
variable "cluster_version" { type = string }
resource "aws_eks_cluster" "this" {
  cluster_version = var.cluster_version # tflens:track
}
`
	const parentSrc = `
locals { cluster_version = "1.34" }
module "eks" {
  source          = "./modules/eks"
  cluster_version = local.cluster_version
}
`
	oldChild := analyseFromTestdata(t, "old/child/main.tf", childOldSrc)
	newChild := analyseFromTestdata(t, "new/child/main.tf", childNewSrc)
	parent := analyseFromTestdata(t, "main.tf", parentSrc)

	got := diff.DiffTrackedCtx(oldChild, newChild, diff.TrackedContext{
		OldParent: parent, NewParent: parent, CallName: "eks",
	})
	if len(got) != 1 || got[0].Kind != diff.Informational {
		t.Errorf("want 1 Informational (marker added, nothing else changed), got %v", got)
	}
}

// TestDiffTrackedCrossModuleEmptyContextEqualsBare proves that
// passing nil parent modules (or omitting the call name) yields the
// exact same result as DiffTracked — i.e. the cross-module support
// is strictly additive when context isn't supplied.
func TestDiffTrackedCrossModuleEmptyContextEqualsBare(t *testing.T) {
	const oldSrc = `
locals {
  cluster_version = "1.34" # tflens:track
}
`
	const newSrc = `
locals {
  cluster_version = "1.35" # tflens:track
}
`
	old := analyseFromTestdata(t, "old/main.tf", oldSrc)
	new := analyseFromTestdata(t, "new/main.tf", newSrc)

	bare := diff.DiffTracked(old, new)
	withCtx := diff.DiffTrackedCtx(old, new, diff.TrackedContext{})
	if len(bare) != len(withCtx) {
		t.Fatalf("len differ: bare=%d ctx=%d", len(bare), len(withCtx))
	}
	for i := range bare {
		if bare[i].Kind != withCtx[i].Kind || bare[i].Detail != withCtx[i].Detail {
			t.Errorf("change[%d] differs: bare=%+v ctx=%+v", i, bare[i], withCtx[i])
		}
	}
}

// Silence unused-import warning if analyseFromTestdata stops needing
// the analysis package directly.
var _ = analysis.KindLocal

// trackedCase mirrors diffCase but drives diff.DiffTracked instead of
// diff.Diff. Reads testdata/<name>/{old.tf,new.tf}.
type trackedCase struct {
	Name           string
	Subject        string // change.Subject (e.g. "resource.aws_eks_cluster.this.cluster_version")
	WantKind       diff.ChangeKind
	DetailContains []string
	HintContains   []string
	WantNoChanges  bool
}

func TestDiffTrackedCases(t *testing.T) {
	for _, tc := range trackedCases {
		t.Run(tc.Name, func(t *testing.T) {
			oldSrc := loadFixture(t, tc.Name, "old.tf")
			newSrc := loadFixture(t, tc.Name, "new.tf")
			changes := diff.DiffTracked(
				analyseFromTestdata(t, "old.tf", oldSrc),
				analyseFromTestdata(t, "new.tf", newSrc),
			)
			if tc.WantNoChanges {
				if len(changes) != 0 {
					t.Fatalf("expected no changes, got: %v", changes)
				}
				return
			}
			c := findChange(changes, tc.Subject)
			if c == nil {
				t.Fatalf("expected change for %q; got: %v", tc.Subject, changes)
			}
			if c.Kind != tc.WantKind {
				t.Errorf("kind = %v, want %v; detail=%q", c.Kind, tc.WantKind, c.Detail)
			}
			for _, sub := range tc.DetailContains {
				if !strings.Contains(c.Detail, sub) {
					t.Errorf("detail should contain %q: %q", sub, c.Detail)
				}
			}
			for _, sub := range tc.HintContains {
				if !strings.Contains(c.Hint, sub) {
					t.Errorf("hint should contain %q: %q", sub, c.Hint)
				}
			}
		})
	}
}

var trackedCases = []trackedCase{
	{
		Name:           "tracked_literal_value_changed",
		Subject:        "resource.aws_eks_cluster.this.cluster_version",
		WantKind:       diff.Breaking,
		DetailContains: []string{"value", "1.28", "1.29"},
		HintContains:   []string{"add-on compatibility"},
	},
	{
		Name:           "tracked_marker_removed",
		Subject:        "resource.aws_eks_cluster.this.cluster_version",
		WantKind:       diff.Breaking,
		DetailContains: []string{"marker removed"},
		HintContains:   []string{"restore"},
	},
	{
		Name:           "tracked_marker_added",
		Subject:        "resource.aws_db_instance.main.engine_version",
		WantKind:       diff.Informational,
		DetailContains: []string{"marker added"},
		HintContains:   []string{"maintenance window"},
	},
	{
		Name:           "tracked_via_variable_default",
		Subject:        "resource.aws_eks_cluster.this.cluster_version",
		WantKind:       diff.Breaking,
		DetailContains: []string{"variable.cluster_version", "1.28", "1.29"},
		HintContains:   []string{"variable default"},
	},
	{
		Name:           "tracked_via_local_value",
		Subject:        "resource.aws_eks_cluster.this.cluster_version",
		WantKind:       diff.Breaking,
		DetailContains: []string{"local.cluster_version", "1.28", "1.29"},
	},
	{
		Name:           "tracked_via_local_chain",
		Subject:        "resource.aws_eks_cluster.this.cluster_version",
		WantKind:       diff.Breaking,
		DetailContains: []string{"local.versions_by_env"},
	},
	{
		Name:          "tracked_no_change",
		WantNoChanges: true,
	},
	{
		Name:           "tracked_own_line_marker",
		Subject:        "resource.aws_eks_cluster.this.cluster_version",
		WantKind:       diff.Breaking,
		DetailContains: []string{"value", "1.28", "1.29"},
	},
	{
		Name:           "tracked_marker_with_description",
		Subject:        "resource.aws_db_instance.main.engine_version",
		WantKind:       diff.Breaking,
		DetailContains: []string{"14.9", "15.4"},
		HintContains:   []string{"maintenance window"},
	},
	{
		Name:          "tracked_marker_no_attribute_match",
		WantNoChanges: true,
	},
	{
		// Marker on a locals-block attribute: each local becomes its
		// own entity (local.<name>) with AttrName = "value". Confirms
		// the locals-block walker binds markers correctly and the
		// regular value-change path fires.
		Name:           "tracked_local_value_changed",
		Subject:        "local.cluster_version.value",
		WantKind:       diff.Breaking,
		DetailContains: []string{"value", "1.34", "1.35"},
		HintContains:   []string{"add-on compat"},
	},
	{
		// Common real-world flow: the marker AND the breaking change
		// are added in the same PR. Used to be Informational (just
		// "marker added"); now it consults the old entity's attribute
		// text and promotes to Breaking when the value also moved.
		Name:           "tracked_marker_added_with_value_change",
		Subject:        "local.cluster_version.value",
		WantKind:       diff.Breaking,
		DetailContains: []string{"marker added", "1.34", "1.35"},
		HintContains:   []string{"add-on compat"},
	},
	{
		// EKS upgrade pattern: old static local; new PR introduces
		// `var.upgrade` (default true) AND makes the local conditional
		// AND adds the marker. Should be Breaking with the variable's
		// active default surfaced inline so reviewers can see which
		// branch of the conditional is live.
		Name:     "tracked_marker_added_via_new_variable",
		Subject:  "local.cluster_version.value",
		WantKind: diff.Breaking,
		DetailContains: []string{
			"marker added",
			`"1.34"`,
			`var.upgrade ? "1.35" : "1.34"`,
			"variable.upgrade", "true",
		},
		HintContains: []string{"add-on compat"},
	},
	{
		// Same scenario but the marker is on the RESOURCE attribute,
		// not the local. The resource attribute's literal text
		// (`local.cluster_version`) doesn't change — the change is
		// indirect, via the local that the resource references and
		// the new variable that the local references. The detection
		// must come from the per-ref comparison rather than the
		// direct attribute text diff.
		Name:     "tracked_marker_added_on_resource_via_indirection",
		Subject:  "resource.aws_eks_cluster.this.cluster_version",
		WantKind: diff.Breaking,
		DetailContains: []string{
			"marker added",
			"local.cluster_version", `"1.34"`, `var.upgrade ? "1.35" : "1.34"`,
			"variable.upgrade", "true",
		},
		HintContains: []string{"add-on compat"},
	},
	{
		// Regression: marker added in a submodule whose resource
		// attribute references a variable with no default. The
		// variable EXISTED in the old version (just without a
		// default), and nothing changed underneath. Used to be
		// reported as Breaking with "now references variable.X =
		// <unset>" because LookupAttrText couldn't tell "entity
		// exists with no default" from "entity doesn't exist".
		// Should now correctly emit Informational only.
		Name:           "tracked_marker_added_no_default_unchanged",
		Subject:        "resource.aws_eks_cluster.this.cluster_version",
		WantKind:       diff.Informational,
		DetailContains: []string{"marker added"},
		HintContains:   []string{"register baseline"},
	},
	{
		// Eval-equivalence: marker stays, but the local's TEXT
		// changes from `"1.34"` to `var.upgrade ? "1.35" : "1.34"`
		// with var.upgrade defaulting to false → both sides evaluate
		// to "1.34". The new variable IS surfaced as a "now
		// references" supporting detail, but the change is NOT
		// Breaking because the effective value didn't move.
		Name:     "tracked_eval_equivalent_conditional_false",
		Subject:  "local.cluster_version.value",
		WantKind: diff.Informational,
		DetailContains: []string{
			"now references variable.upgrade", "false",
		},
	},
	{
		// Same shape as above but var.upgrade defaults to true →
		// the conditional resolves to "1.35", a real change. Should
		// be Breaking with the value diff surfaced.
		Name:     "tracked_eval_real_change_true",
		Subject:  "local.cluster_version.value",
		WantKind: diff.Breaking,
		DetailContains: []string{
			`"1.34"`, `var.upgrade ? "1.35" : "1.34"`,
			"now references variable.upgrade", "true",
		},
	},
	{
		// Force-new attribute case: cluster_name = "${var.env}-${local.suffix}".
		// Only local.suffix changes between revisions; the literal text of
		// the attribute is unchanged. The tracked-attribute pass must
		// follow string-interpolation traversals, otherwise this kind of
		// destroy/recreate trigger goes undetected.
		Name:           "tracked_via_string_interpolation",
		Subject:        "resource.aws_eks_cluster.this.cluster_name",
		WantKind:       diff.Breaking,
		DetailContains: []string{"local.suffix", "primary", "secondary"},
		HintContains:   []string{"force-new"},
	},
}
