package diff_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/diff"
)

// crossModuleCase drives TestDiffTrackedCrossModuleCases. Each case
// names a directory under pkg/diff/testdata/cross_module_tracked/<name>/
// laid out as a real Terraform project:
//
//	<name>/old/main.tf                 (parent / root)
//	<name>/old/modules/<call>/main.tf  (child module — where the marker lives)
//	<name>/new/main.tf
//	<name>/new/modules/<call>/main.tf
//
// CallName is the module call's local name in the parent
// (`module "<call>" { ... }`). Empty defaults to "eks".
type crossModuleCase struct {
	Name           string
	CallName       string
	WantKind       diff.ChangeKind
	Subject        string // tracked attr key, e.g. "resource.aws_eks_cluster.this.cluster_version"
	DetailContains []string
	DetailExcludes []string
	HintContains   []string
}

func TestDiffTrackedCrossModuleCases(t *testing.T) {
	for _, tc := range crossModuleCases {
		t.Run(tc.Name, func(t *testing.T) {
			callName := tc.CallName
			if callName == "" {
				callName = "eks"
			}
			oldParent, oldChild := loadCrossModuleSide(t, tc.Name, "old", callName)
			newParent, newChild := loadCrossModuleSide(t, tc.Name, "new", callName)

			got := diff.DiffTrackedCtx(oldChild, newChild, diff.TrackedContext{
				OldParent: oldParent,
				NewParent: newParent,
				CallName:  callName,
			})
			if len(got) != 1 {
				t.Fatalf("want 1 change, got %d: %v", len(got), got)
			}
			c := got[0]
			if c.Subject != tc.Subject {
				t.Errorf("Subject = %q, want %q", c.Subject, tc.Subject)
			}
			if c.Kind != tc.WantKind {
				t.Errorf("Kind = %v, want %v; detail=%q", c.Kind, tc.WantKind, c.Detail)
			}
			for _, s := range tc.DetailContains {
				if !strings.Contains(c.Detail, s) {
					t.Errorf("detail missing %q: %s", s, c.Detail)
				}
			}
			for _, s := range tc.DetailExcludes {
				if strings.Contains(c.Detail, s) {
					t.Errorf("detail should not contain %q: %s", s, c.Detail)
				}
			}
			for _, s := range tc.HintContains {
				if !strings.Contains(c.Hint, s) {
					t.Errorf("hint missing %q: %s", s, c.Hint)
				}
			}
		})
	}
}

// loadCrossModuleSide reads {parent main.tf, child modules/<call>/main.tf}
// for one side (old or new) of a cross_module_tracked fixture and
// returns the analysed modules.
func loadCrossModuleSide(t *testing.T, name, side, callName string) (parent, child *analysis.Module) {
	t.Helper()
	parentPath := filepath.Join("testdata", "cross_module_tracked", name, side, "main.tf")
	childPath := filepath.Join("testdata", "cross_module_tracked", name, side, "modules", callName, "main.tf")
	parentSrc := mustReadFile(t, parentPath)
	childSrc := mustReadFile(t, childPath)
	parent = analyseFromTestdata(t, parentPath, parentSrc)
	child = analyseFromTestdata(t, childPath, childSrc)
	return parent, child
}

func mustReadFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

var crossModuleCases = []crossModuleCase{
	{
		// Marker added in child + parent introduces var.upgrade
		// defaulting to true → conditional flips the cluster version
		// from "1.34" to "1.35". Real value change → Breaking.
		Name:     "parent_change_real",
		Subject:  "resource.aws_eks_cluster.this.cluster_version",
		WantKind: diff.Breaking,
		DetailContains: []string{
			"marker added",
			"parent.local.cluster_version", `"1.34"`, `var.upgrade ? "1.35" : "1.34"`,
			"parent.variable.upgrade", "true",
		},
		HintContains: []string{"add-on compat"},
	},
	{
		// Same shape but var.upgrade defaults to FALSE → conditional
		// resolves to "1.34", same as the old static value. The
		// effective tracked value didn't move; eval suppression
		// should demote to Informational with the new variable as
		// supporting context. Mirrors the "stage the upgrade flag
		// without flipping it yet" workflow. The detail must
		// include the "text changes collapsed" clause so a reviewer
		// understands WHY this isn't Breaking even though a new
		// variable appeared.
		Name:     "parent_change_eval_unchanged",
		Subject:  "resource.aws_eks_cluster.this.cluster_version",
		WantKind: diff.Informational,
		DetailContains: []string{
			"marker added",
			"text changes collapsed",
			"same effective value",
			"now references parent.variable.upgrade", "false",
		},
		DetailExcludes: []string{
			"parent.local.cluster_version changed",
		},
		HintContains: []string{"add-on compat"},
	},
	{
		// Sanity check: marker added in child, parent identical on
		// both sides. No false positives from cross-module
		// resolution — Informational only.
		Name:           "no_parent_change",
		Subject:        "resource.aws_eks_cluster.this.cluster_version",
		WantKind:       diff.Informational,
		DetailContains: []string{"marker added"},
	},
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
		// Stdlib batch 1 (toset): old + new produce the same effective
		// set via different toset() input shapes. Texts differ but the
		// evaluated values match → Informational, not Breaking.
		Name:           "tracked_eval_toset_reorder",
		Subject:        "local.ids.value",
		WantKind:       diff.Informational,
		DetailContains: []string{"no effective value change"},
	},
	{
		// Stdlib batch 1 (merge): the new side restructures the same
		// key/value pairs across two merge() arguments. Effective
		// object identical → Informational.
		Name:           "tracked_eval_merge_restructured",
		Subject:        "local.tags.value",
		WantKind:       diff.Informational,
		DetailContains: []string{"no effective value change"},
	},
	{
		// Stdlib batch 1 (concat true positive): both sides go through
		// concat() but the new side adds an element. The evaluated
		// values legitimately differ → still Breaking. Pin so the new
		// value-equivalent short-circuit doesn't over-suppress real
		// changes.
		Name:           "tracked_eval_length_change_breaking",
		Subject:        "local.ids.value",
		WantKind:       diff.Breaking,
		DetailContains: []string{"value"},
	},
	{
		// Stdlib batch 2 (lower): refactor lowercases a constant via
		// lower() instead of typing the canonical form. Effective
		// string identical → Informational, not Breaking.
		Name:           "tracked_eval_lower_collapses",
		Subject:        "local.region.value",
		WantKind:       diff.Informational,
		DetailContains: []string{"no effective value change"},
	},
	{
		// Stdlib batch 2 (format): same string assembled via format()
		// rather than inlined. Effective value unchanged →
		// Informational. Verifies %s + %d formatters round-trip.
		Name:           "tracked_eval_format_collapses",
		Subject:        "local.image.value",
		WantKind:       diff.Informational,
		DetailContains: []string{"no effective value change"},
	},
	{
		// Stdlib batch 2.5 (regex): refactor pulls a substring via
		// regex() rather than inlining the literal. Effective value
		// identical → Informational. Pins regex(pattern, string)
		// no-capture-group return shape (string, not tuple/object).
		Name:           "tracked_eval_regex_collapses",
		Subject:        "local.major.value",
		WantKind:       diff.Informational,
		DetailContains: []string{"no effective value change"},
	},
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
		// Eval graceful-degradation: the local references a data
		// source (data.aws_caller_identity.current.account_id) that
		// can't be statically evaluated — `data.X` isn't bound in
		// our EvalContext. equivalentByEval returns false on both
		// sides, so we fall back to literal text comparison. The
		// suffix changed from "-prod" to "-staging" so the texts
		// differ → reported as Breaking, conservatively. (We can't
		// PROVE the value didn't change without resolving the data
		// lookup, so we err on the side of flagging.)
		Name:     "tracked_eval_data_ref_unevaluable",
		Subject:  "local.cluster_name.value",
		WantKind: diff.Breaking,
		DetailContains: []string{
			`-prod`, `-staging`,
		},
	},
	{
		// Single-module mirror of the cross-module
		// var.upgrade=false scenario: marker added on the resource
		// attribute, parent introduces a new variable + conditional
		// local. With var.upgrade=false the conditional resolves to
		// "1.34" (same as old), so eval suppression should kick in
		// and demote to Informational. Confirms the resource-level
		// marker path participates in the eval-equivalence collapse,
		// not just the local-level one.
		Name:     "tracked_marker_added_on_resource_eval_unchanged",
		Subject:  "resource.aws_eks_cluster.this.cluster_version",
		WantKind: diff.Informational,
		DetailContains: []string{
			"marker added",
			"text changes collapsed",
			"now references variable.upgrade", "false",
		},
		HintContains: []string{"add-on compat"},
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
