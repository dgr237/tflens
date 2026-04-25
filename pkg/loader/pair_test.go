package loader_test

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/dgr237/tflens/pkg/loader"
)

func TestModuleCallStatusString(t *testing.T) {
	cases := map[loader.ModuleCallStatus]string{
		loader.StatusChanged:        "changed",
		loader.StatusAdded:          "added",
		loader.StatusRemoved:        "removed",
		loader.ModuleCallStatus(99): "changed", // unknown falls back to "changed"
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("Status(%d).String() = %q, want %q", s, got, want)
		}
	}
}

// pairFixtureDir returns the absolute path to
// pkg/loader/testdata/pair/<case>/<side>. Returns "" when the side
// directory doesn't exist (so the case can express "no project on
// this side" by simply omitting the directory).
func pairFixtureDir(t *testing.T, name, side string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	abs, err := filepath.Abs(filepath.Join(filepath.Dir(file), "testdata", "pair", name, side))
	if err != nil {
		t.Fatalf("resolving fixture %s/%s: %v", name, side, err)
	}
	if _, err := os.Stat(abs); os.IsNotExist(err) {
		return ""
	}
	return abs
}

// loadPairSide loads the named side ("old" / "new") of a pair fixture,
// returning nil when the side dir doesn't exist (the "all-Added" /
// "all-Removed" cases).
func loadPairSide(t *testing.T, casename, side string) *loader.Project {
	t.Helper()
	dir := pairFixtureDir(t, casename, side)
	if dir == "" {
		return nil
	}
	proj, _, err := loader.LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject(%s/%s): %v", casename, side, err)
	}
	return proj
}

// pairCase pairs an old/new fixture pair with assertions on the
// resulting []ModuleCallPair. Either side's directory may be absent,
// expressing the all-Added or all-Removed scenarios.
type pairCase struct {
	Name   string
	Custom func(t *testing.T, pairs []loader.ModuleCallPair)
}

func TestPairModuleCallsCases(t *testing.T) {
	for _, tc := range pairCases {
		t.Run(tc.Name, func(t *testing.T) {
			old := loadPairSide(t, tc.Name, "old")
			newp := loadPairSide(t, tc.Name, "new")
			tc.Custom(t, loader.PairModuleCalls(old, newp))
		})
	}
}

var pairCases = []pairCase{
	{
		// All-nil safety case — no panic, empty result. No fixture
		// directory exists for either side; both sides resolve to nil.
		Name: "both_nil",
		Custom: func(t *testing.T, pairs []loader.ModuleCallPair) {
			if len(pairs) != 0 {
				t.Errorf("nil/nil: got %d pairs, want 0", len(pairs))
			}
		},
	},
	{
		// Project only exists on the new side. All pairs should be
		// StatusAdded with only New* fields populated.
		Name: "added_only",
		Custom: func(t *testing.T, pairs []loader.ModuleCallPair) {
			if len(pairs) != 1 {
				t.Fatalf("got %d pairs, want 1", len(pairs))
			}
			p := pairs[0]
			if p.Key != "vpc" || p.LocalName != "vpc" {
				t.Errorf("Key/LocalName = %q/%q, want vpc/vpc", p.Key, p.LocalName)
			}
			if p.Status != loader.StatusAdded {
				t.Errorf("Status = %v, want StatusAdded", p.Status)
			}
			if p.NewSource != "ns/vpc/aws" || p.NewVersion != "1.0.0" {
				t.Errorf("NewSource/NewVersion = %q/%q", p.NewSource, p.NewVersion)
			}
			if p.OldSource != "" || p.OldVersion != "" {
				t.Errorf("Old fields should be empty; got %q/%q", p.OldSource, p.OldVersion)
			}
		},
	},
	{
		// Project only exists on the old side. All pairs should be
		// StatusRemoved.
		Name: "removed_only",
		Custom: func(t *testing.T, pairs []loader.ModuleCallPair) {
			if len(pairs) != 1 {
				t.Fatalf("got %d pairs, want 1", len(pairs))
			}
			if pairs[0].Status != loader.StatusRemoved {
				t.Errorf("Status = %v, want StatusRemoved", pairs[0].Status)
			}
			if pairs[0].OldSource != "ns/vpc/aws" {
				t.Errorf("OldSource = %q, want ns/vpc/aws", pairs[0].OldSource)
			}
		},
	},
	{
		// Same key in both sides, with different sources/versions.
		// Both Old* and New* fields populated.
		Name: "changed_across_sides",
		Custom: func(t *testing.T, pairs []loader.ModuleCallPair) {
			if len(pairs) != 1 {
				t.Fatalf("got %d pairs, want 1", len(pairs))
			}
			p := pairs[0]
			if p.Status != loader.StatusChanged {
				t.Errorf("Status = %v, want StatusChanged", p.Status)
			}
			if p.OldVersion != "1.0.0" || p.NewVersion != "2.0.0" {
				t.Errorf("versions: old=%q new=%q, want 1.0.0/2.0.0", p.OldVersion, p.NewVersion)
			}
		},
	},
	{
		// Adds + removes mixed in the same diff to confirm the union
		// over keys works correctly.
		Name: "added_removed_together",
		Custom: func(t *testing.T, pairs []loader.ModuleCallPair) {
			sort.Slice(pairs, func(i, j int) bool { return pairs[i].Key < pairs[j].Key })
			if len(pairs) != 3 {
				t.Fatalf("got %d pairs, want 3", len(pairs))
			}
			wantStatuses := map[string]loader.ModuleCallStatus{
				"added_one":   loader.StatusAdded,
				"kept":        loader.StatusChanged,
				"removed_one": loader.StatusRemoved,
			}
			for _, p := range pairs {
				want, ok := wantStatuses[p.Key]
				if !ok {
					t.Errorf("unexpected key: %q", p.Key)
					continue
				}
				if p.Status != want {
					t.Errorf("%q: Status = %v, want %v", p.Key, p.Status, want)
				}
			}
		},
	},
	{
		// Nested modules produce dotted keys and are paired
		// independently. Only the new side has a project — verifies
		// recursion through ./modules/vpc → ./sg.
		Name: "recurses_into_children",
		Custom: func(t *testing.T, pairs []loader.ModuleCallPair) {
			keys := make([]string, len(pairs))
			for i, p := range pairs {
				keys[i] = p.Key
			}
			sort.Strings(keys)
			want := []string{"vpc", "vpc.sg"}
			if len(keys) != len(want) || keys[0] != want[0] || keys[1] != want[1] {
				t.Errorf("keys = %v, want %v", keys, want)
			}
		},
	},
}
