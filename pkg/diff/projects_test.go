package diff_test

import (
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"

	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
)

// projectFixtureDir returns the absolute path to
// pkg/diff/testdata/projects/<case>/<side>. Returns "" when the side
// directory doesn't exist (so a case can express "no project on this
// side" by simply omitting the directory).
func projectFixtureDir(t *testing.T, name, side string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	abs, err := filepath.Abs(filepath.Join(filepath.Dir(file), "testdata", "projects", name, side))
	if err != nil {
		t.Fatalf("resolving fixture %s/%s: %v", name, side, err)
	}
	if _, err := os.Stat(abs); os.IsNotExist(err) {
		return ""
	}
	return abs
}

// loadProjectSide loads the named side ("old" / "new") of a projects
// fixture. Returns nil when the side dir doesn't exist (e.g. "added
// only" cases that have no old side).
func loadProjectSide(t *testing.T, casename, side string) *loader.Project {
	t.Helper()
	dir := projectFixtureDir(t, casename, side)
	if dir == "" {
		return nil
	}
	proj, _, err := loader.LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject(%s/%s): %v", casename, side, err)
	}
	return proj
}

// projectCase pairs a fixture pair with custom assertions over the
// (old, new) projects. Either side may be absent (loaded as nil).
type projectCase struct {
	Name   string
	Custom func(t *testing.T, old, new *loader.Project)
}

func TestAnalyzeProjectsCases(t *testing.T) {
	for _, tc := range projectCases {
		t.Run(tc.Name, func(t *testing.T) {
			old := loadProjectSide(t, tc.Name, "old")
			newp := loadProjectSide(t, tc.Name, "new")
			tc.Custom(t, old, newp)
		})
	}
}

var projectCases = []projectCase{
	{
		// nil/nil produces an empty result with no breaking count —
		// the trivial nil-safety case for the cmd layer. No fixture
		// directories exist for this case.
		Name: "empty_both_sides",
		Custom: func(t *testing.T, old, newp *loader.Project) {
			results, rootChanges, breaking := diff.AnalyzeProjects(old, newp)
			if len(results) != 0 || len(rootChanges) != 0 || breaking != 0 {
				t.Errorf("nil/nil: results=%d rootChanges=%d breaking=%d",
					len(results), len(rootChanges), breaking)
			}
		},
	},
	{
		// A required variable added to the root counts as Breaking
		// and bumps the tally.
		Name: "root_diff_breaking",
		Custom: func(t *testing.T, old, newp *loader.Project) {
			_, rootChanges, breaking := diff.AnalyzeProjects(old, newp)
			if len(rootChanges) == 0 {
				t.Fatal("expected at least one root change")
			}
			found := false
			for _, c := range rootChanges {
				if c.Subject == "variable.required" && c.Kind == diff.Breaking {
					found = true
				}
			}
			if !found {
				t.Errorf("missing Breaking variable.required; got: %v", rootChanges)
			}
			if breaking == 0 {
				t.Error("breaking count should be > 0")
			}
		},
	},
	{
		// Pair results come back in alphabetical-by-Key order so the
		// cmd layer's rendering is deterministic. Diff against self
		// for the ordering check.
		Name: "results_sorted_by_key",
		Custom: func(t *testing.T, old, _ *loader.Project) {
			results, _, _ := diff.AnalyzeProjects(old, old)
			keys := make([]string, len(results))
			for i, r := range results {
				keys[i] = r.Pair.Key
			}
			if !sort.StringsAreSorted(keys) {
				t.Errorf("results not sorted by Key: %v", keys)
			}
		},
	},
	{
		// only="vpc" restricts results to that one call. Verifies
		// the `only` filter path used by cmd/whatif for the optional
		// positional name arg.
		Name: "whatif_filters_by_only_name",
		Custom: func(t *testing.T, old, newp *loader.Project) {
			calls, _, filteredOut := diff.AnalyzeWhatif(old, newp, "vpc")
			if filteredOut {
				t.Fatal("expected filteredOut=false when 'vpc' matches a real call")
			}
			if len(calls) != 1 {
				t.Errorf("expected 1 call, got %d", len(calls))
			}
			if calls[0].Pair.Key != "vpc" {
				t.Errorf("filtered call = %q, want vpc", calls[0].Pair.Key)
			}
		},
	},
	{
		// only="doesnotexist" returns filteredOut=true so the cmd
		// layer can produce a user-friendly "no module call named X
		// differs" error.
		Name: "whatif_filtered_out_no_match",
		Custom: func(t *testing.T, old, newp *loader.Project) {
			calls, _, filteredOut := diff.AnalyzeWhatif(old, newp, "doesnotexist")
			if !filteredOut {
				t.Error("expected filteredOut=true for non-matching name")
			}
			if calls != nil {
				t.Errorf("expected nil calls, got %v", calls)
			}
		},
	},
	{
		// A call that's only on the new side has no base-side caller
		// to validate against, so AnalyzeWhatif silently skips it
		// rather than producing a noisy "REMOVED" or half-empty entry.
		Name: "whatif_skips_added_calls",
		Custom: func(t *testing.T, old, newp *loader.Project) {
			calls, _, _ := diff.AnalyzeWhatif(old, newp, "")
			for _, c := range calls {
				if c.Pair.Status == loader.StatusAdded {
					t.Errorf("AnalyzeWhatif should skip added calls, got: %+v", c)
				}
			}
		},
	},
	{
		// Removing a variable that the parent passes to a local-source
		// child is breaking from the consumer's view. Exercises
		// changesForPair's local-source branch
		// (ConsumptionChangesForLocal) end-to-end via AnalyzeProjects.
		Name: "local_child_breaking_change",
		Custom: func(t *testing.T, old, newp *loader.Project) {
			results, _, breaking := diff.AnalyzeProjects(old, newp)
			if breaking == 0 {
				t.Error("expected breakingCount > 0 from removed child variable")
			}
			var kid *diff.PairResult
			for i := range results {
				if results[i].Pair.Key == "kid" {
					kid = &results[i]
				}
			}
			if kid == nil {
				t.Fatal("missing PairResult for 'kid'")
			}
			if len(kid.Changes) == 0 {
				t.Error("expected changes for the kid call")
			}
		},
	},
	{
		// When a registry-style child can't be resolved (no node on
		// either side), changesForPair is skipped and AnalyzeProjects
		// still returns a result with empty Changes. Verifies the
		// OldNode/NewNode nil guard in projects.go.
		Name: "registry_child_version_bump",
		Custom: func(t *testing.T, old, newp *loader.Project) {
			results, _, _ := diff.AnalyzeProjects(old, newp)
			var reg *diff.PairResult
			for i := range results {
				if results[i].Pair.Key == "reg" {
					reg = &results[i]
				}
			}
			if reg == nil {
				t.Fatal("missing PairResult for 'reg'")
			}
			if len(reg.Changes) != 0 {
				t.Errorf("expected no per-call changes for unresolved registry call, got %v",
					reg.Changes)
			}
		},
	},
}
