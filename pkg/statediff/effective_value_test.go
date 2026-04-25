package statediff_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/statediff"
)

// effectiveValueCase pairs an old/new fixture pair under
// pkg/statediff/testdata/effective_value_collapse/<Name>/{main,feature}/main.tf
// with an assertion on whether the change should be flagged. The
// new value-equality short-circuit in diffValues collapses text-
// different / value-identical sensitive locals; these tests pin
// both directions:
//
//   - WantFlagged=false: text differs but the cty.Value is identical
//     (sort, distinct on already-sorted, merge, toset reorder) →
//     suppressed, no SensitiveChange emitted.
//   - WantFlagged=true: the value actually changed (length up/down,
//     element added/removed) → still flagged. Pin so the new
//     short-circuit doesn't accidentally over-suppress.
type effectiveValueCase struct {
	Name        string
	WantFlagged bool
}

func TestStatediffEffectiveValueCollapse(t *testing.T) {
	for _, tc := range effectiveValueCases {
		t.Run(tc.Name, func(t *testing.T) {
			oldP := loadEffectiveValueFixture(t, tc.Name, "main")
			newP := loadEffectiveValueFixture(t, tc.Name, "feature")
			r := statediff.Analyze(oldP, newP, nil)
			if tc.WantFlagged && len(r.SensitiveChanges) == 0 {
				t.Errorf("expected SensitiveChange to fire; got none")
			}
			if !tc.WantFlagged && len(r.SensitiveChanges) != 0 {
				t.Errorf("expected no SensitiveChange (text differs but value matches); got: %+v",
					r.SensitiveChanges)
			}
		})
	}
}

var effectiveValueCases = []effectiveValueCase{
	{
		// Both branches define the same effective list value via
		// different expressions (literal vs distinct-of-already-
		// distinct). New value-equality check suppresses.
		Name: "sort_doesnt_flag",
	},
	{
		// toset() folds duplicates and discards order. Text is
		// noisily different but the resulting set is identical.
		Name: "toset_reorder_doesnt_flag",
	},
	{
		// merge() across multiple objects with the same key/value
		// pairs as the original literal — equivalent object value.
		Name: "merge_same_value_doesnt_flag",
	},
	{
		// True positive: an element was actually added. Confirm
		// the suppression doesn't over-fire.
		Name:        "length_change_still_flags",
		WantFlagged: true,
	},
	{
		// Stdlib batch 2 (lower): canonical form vs lower("US-EAST-1")
		// — same effective string. Local reaches for_each but no
		// SensitiveChange should fire.
		Name: "lower_collapses",
	},
	{
		// Stdlib batch 2 (format): same string assembled via
		// format("ec2-%s-v%d", "small", 3) vs literal. Effective value
		// identical → suppressed.
		Name: "format_collapses",
	},
}

func loadEffectiveValueFixture(t *testing.T, caseName, side string) *loader.Project {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Join(filepath.Dir(file), "testdata", "effective_value_collapse", caseName, side)
	if _, err := os.Stat(dir); err != nil {
		t.Fatalf("fixture %s/%s missing: %v", caseName, side, err)
	}
	p, _, err := loader.LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject(%s/%s): %v", caseName, side, err)
	}
	return p
}
