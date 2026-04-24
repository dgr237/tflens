package diff_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/dgr237/tflens/pkg/diff"
	"github.com/dgr237/tflens/pkg/loader"
)

// loadDiffProj writes src to a temp dir and loads it as a Project.
// Convenience for AnalyzeProjects / AnalyzeWhatif tests.
func loadDiffProj(t *testing.T, src string) *loader.Project {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, _, err := loader.LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	return proj
}

// TestAnalyzeProjectsEmptyBothSides: nil/nil produces an empty
// result with no breaking count. Used as the trivial nil-safety case
// for the cmd layer.
func TestAnalyzeProjectsEmptyBothSides(t *testing.T) {
	results, rootChanges, breaking := diff.AnalyzeProjects(nil, nil)
	if len(results) != 0 || len(rootChanges) != 0 || breaking != 0 {
		t.Errorf("nil/nil: results=%d rootChanges=%d breaking=%d", len(results), len(rootChanges), breaking)
	}
}

// TestAnalyzeProjectsRootDiffContributesToBreaking: a required
// variable added to the root counts as Breaking and bumps the tally.
func TestAnalyzeProjectsRootDiffContributesToBreaking(t *testing.T) {
	old := loadDiffProj(t, `# empty root`)
	new := loadDiffProj(t, `variable "required" { type = string }`)

	_, rootChanges, breaking := diff.AnalyzeProjects(old, new)
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
}

// TestAnalyzeProjectsResultsSortedByKey: pair results come back in
// alphabetical-by-Key order so the cmd layer's rendering is
// deterministic.
func TestAnalyzeProjectsResultsSortedByKey(t *testing.T) {
	old := loadDiffProj(t, `
module "z_one" { source = "ns/x/aws" }
module "a_two" { source = "ns/x/aws" }
module "m_mid" { source = "ns/x/aws" }
`)
	results, _, _ := diff.AnalyzeProjects(old, old) // diff against self for ordering check
	keys := make([]string, len(results))
	for i, r := range results {
		keys[i] = r.Pair.Key
	}
	if !sort.StringsAreSorted(keys) {
		t.Errorf("results not sorted by Key: %v", keys)
	}
}

// TestAnalyzeWhatifFiltersByOnlyName: passing only="vpc" restricts
// results to that one call. Verifies the `only` filter path that
// cmd/whatif uses for the optional positional name arg.
func TestAnalyzeWhatifFiltersByOnlyName(t *testing.T) {
	old := loadDiffProj(t, `
module "vpc" { source = "ns/vpc/aws", version = "1.0.0" }
module "rds" { source = "ns/rds/aws", version = "1.0.0" }
`)
	new := loadDiffProj(t, `
module "vpc" { source = "ns/vpc/aws", version = "2.0.0" }
module "rds" { source = "ns/rds/aws", version = "1.0.0" }
`)

	calls, _, filteredOut := diff.AnalyzeWhatif(old, new, "vpc")
	if filteredOut {
		t.Fatal("expected filteredOut=false when 'vpc' matches a real call")
	}
	if len(calls) != 1 {
		t.Errorf("expected 1 call, got %d", len(calls))
	}
	if calls[0].Pair.Key != "vpc" {
		t.Errorf("filtered call = %q, want vpc", calls[0].Pair.Key)
	}
}

// TestAnalyzeWhatifFilteredOutWhenNoMatch: only="nope" returns
// filteredOut=true so the cmd layer can produce a user-friendly
// "no module call named X differs" error.
func TestAnalyzeWhatifFilteredOutWhenNoMatch(t *testing.T) {
	old := loadDiffProj(t, `module "vpc" { source = "ns/vpc/aws", version = "1.0.0" }`)
	new := loadDiffProj(t, `module "vpc" { source = "ns/vpc/aws", version = "2.0.0" }`)

	calls, _, filteredOut := diff.AnalyzeWhatif(old, new, "doesnotexist")
	if !filteredOut {
		t.Error("expected filteredOut=true for non-matching name")
	}
	if calls != nil {
		t.Errorf("expected nil calls, got %v", calls)
	}
}

// TestAnalyzeWhatifSkipsAddedCalls: a call that's only on the new
// side has no base-side caller to validate against, so AnalyzeWhatif
// silently skips it (rather than producing a noisy "REMOVED" or
// half-empty entry).
func TestAnalyzeWhatifSkipsAddedCalls(t *testing.T) {
	old := loadDiffProj(t, `# empty`)
	new := loadDiffProj(t, `module "fresh" { source = "ns/x/aws", version = "1.0.0" }`)

	calls, _, _ := diff.AnalyzeWhatif(old, new, "")
	for _, c := range calls {
		if c.Pair.Status == loader.StatusAdded {
			t.Errorf("AnalyzeWhatif should skip added calls, got: %+v", c)
		}
	}
}

// loadDiffProjWithChild is loadDiffProj with a single ./child sibling
// directory, so the loaded project actually has a resolved child node
// for the call. Lets tests exercise changesForPair, which only fires
// for StatusChanged pairs that have both OldNode and NewNode populated.
func loadDiffProjWithChild(t *testing.T, rootSrc, childSrc string) *loader.Project {
	t.Helper()
	dir := t.TempDir()
	childDir := filepath.Join(dir, "child")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childDir, "main.tf"), []byte(childSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(rootSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, _, err := loader.LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	return proj
}

// TestAnalyzeProjectsLocalChildBreakingChange: removing a variable
// that the parent passes to a local-source child is breaking from
// the consumer's view. Exercises changesForPair's local-source
// branch (ConsumptionChangesForLocal) end-to-end via AnalyzeProjects.
func TestAnalyzeProjectsLocalChildBreakingChange(t *testing.T) {
	old := loadDiffProjWithChild(t,
		`module "kid" {
  source = "./child"
  x      = "hello"
}`,
		`variable "x" { type = string }`,
	)
	new := loadDiffProjWithChild(t,
		`module "kid" {
  source = "./child"
  x      = "hello"
}`,
		`# variable removed`,
	)

	results, _, breaking := diff.AnalyzeProjects(old, new)
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
}

// TestAnalyzeProjectsRegistryChildVersionBump: when a registry-style
// child can't be resolved (no node on either side), changesForPair
// is skipped and AnalyzeProjects still returns a result with empty
// Changes. Verifies the OldNode/NewNode nil guard in projects.go.
func TestAnalyzeProjectsRegistryChildVersionBump(t *testing.T) {
	old := loadDiffProj(t, `module "reg" { source = "ns/x/aws", version = "1.0.0" }`)
	new := loadDiffProj(t, `module "reg" { source = "ns/x/aws", version = "2.0.0" }`)

	results, _, _ := diff.AnalyzeProjects(old, new)
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
		t.Errorf("expected no per-call changes for unresolved registry call, got %v", reg.Changes)
	}
}
