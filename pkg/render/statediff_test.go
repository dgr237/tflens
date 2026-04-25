package render_test

import (
	"bytes"
	"testing"

	"github.com/dgr237/tflens/pkg/statediff"
)

// statediffCase pairs a *statediff.Result literal with a golden file
// holding the expected rendered output. testdata/statediff/<Name>.golden.
//
// Adding a case: append the struct entry, then run with -update to
// regenerate the golden, review the diff, and commit both.
//
// Result is a pointer so a nil entry is a valid test (nil-safety).
type statediffCase struct {
	Name   string
	Result *statediff.Result
}

func TestRendererStatediffCases(t *testing.T) {
	for _, tc := range statediffCases {
		t.Run(tc.Name, func(t *testing.T) {
			var b bytes.Buffer
			consoleRenderer(&b).Statediff(tc.Result)
			checkGolden(t, "statediff", tc.Name, b.Bytes())
		})
	}
}

var statediffCases = []statediffCase{
	{
		// nil result writes nothing — pure nil-safety check. The
		// golden is an empty file.
		Name: "nil_result",
	},
	{
		// No flagged changes + no orphans → "no resource identity or
		// sensitive-local changes detected vs <ref>" baseline.
		Name:   "empty_baseline",
		Result: &statediff.Result{BaseRef: "main"},
	},
	{
		// Two adds + one remove under the canonical "Resource identity
		// changes vs <ref>:" heading. Module-prefixed addresses appear
		// alongside top-level ones.
		Name: "added_and_removed_resources",
		Result: &statediff.Result{
			BaseRef: "main",
			AddedResources: []statediff.ResourceRef{
				{Type: "aws_vpc", Name: "main", Mode: "managed"},
				{Module: "module.app", Type: "aws_instance", Name: "web", Mode: "managed"},
			},
			RemovedResources: []statediff.ResourceRef{
				{Type: "aws_subnet", Name: "old", Mode: "managed"},
			},
		},
	},
	{
		// Renames have their own labelled section after the identity-
		// changes section. Address arrows pin the visual format.
		Name: "renames_under_own_heading",
		Result: &statediff.Result{
			BaseRef: "main",
			RenamedResources: []statediff.RenamePair{
				{Module: "module.vpc", From: "resource.aws_subnet.old", To: "resource.aws_subnet.new"},
			},
		},
	},
	{
		// Sensitive change without state instances: shows old/new +
		// affected resources but no per-instance bullets.
		Name: "sensitive_change_no_state_instances",
		Result: &statediff.Result{
			BaseRef: "main",
			SensitiveChanges: []statediff.SensitiveChange{{
				Kind: "local", Name: "enabled",
				OldValue: "1", NewValue: "0",
				AffectedResources: []statediff.AffectedResource{
					{Type: "aws_vpc", Name: "main", MetaArg: "count"},
				},
			}},
		},
	},
	{
		// Sensitive change WITH state instances: per-instance bullets
		// appear under the affected resource.
		Name: "sensitive_change_with_state_instances",
		Result: &statediff.Result{
			BaseRef: "main",
			SensitiveChanges: []statediff.SensitiveChange{{
				Kind: "local", Name: "regions",
				OldValue: `["us-east-1", "us-west-2"]`,
				NewValue: `["us-east-1"]`,
				AffectedResources: []statediff.AffectedResource{{
					Type: "aws_vpc", Name: "main", MetaArg: "for_each",
					StateInstances: []string{
						`aws_vpc.main["us-east-1"]`,
						`aws_vpc.main["us-west-2"]`,
					},
				}},
			}},
		},
	},
	{
		// SensitiveChange whose Module field is set — the prefix
		// becomes "<module>.<kind>.<name>" instead of "<kind>.<name>".
		// Pins the module-scoped local/variable rendering.
		Name: "sensitive_change_module_scoped",
		Result: &statediff.Result{
			BaseRef: "main",
			SensitiveChanges: []statediff.SensitiveChange{{
				Module: "module.app", Kind: "local", Name: "size",
				OldValue: "3", NewValue: "1",
			}},
		},
	},
	{
		// Empty NewValue renders as "(absent)" — distinguishes
		// "default removed" from "default = ''".
		Name: "or_absent_marker",
		Result: &statediff.Result{
			BaseRef: "main",
			SensitiveChanges: []statediff.SensitiveChange{
				{Kind: "variable", Name: "n", OldValue: "3", NewValue: ""},
			},
		},
	},
	{
		// Only state orphans + no other findings. The orphan section
		// renders but the "no changes detected" baseline is suppressed
		// (because there ARE orphans).
		Name: "state_orphans_only",
		Result: &statediff.Result{
			BaseRef:      "main",
			StateOrphans: []string{"aws_eip.unused", `aws_subnet.old["a"]`},
		},
	},
	{
		// Two distinct sections (adds + renames) — pin the inter-
		// section blank-line separator.
		Name: "sections_separated_by_blank_line",
		Result: &statediff.Result{
			BaseRef:        "main",
			AddedResources: []statediff.ResourceRef{{Type: "aws_vpc", Name: "main", Mode: "managed"}},
			RenamedResources: []statediff.RenamePair{
				{From: "resource.aws_subnet.old", To: "resource.aws_subnet.new"},
			},
		},
	},
}
