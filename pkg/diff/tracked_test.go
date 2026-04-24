package diff_test

import (
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/diff"
)

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
