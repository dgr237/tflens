package loader_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dgr237/tflens/pkg/loader"
)

// TestDefaultResolverChainOfflineSkipsNetworkResolvers verifies the
// offline=true path returns a chain that contains only the manifest
// + local-path resolvers. The chain itself is opaque (resolver.Chain),
// so we exercise it indirectly: a registry source produces no child
// (would need the registry resolver), a local source still resolves.
func TestDefaultResolverChainOfflineSkipsNetworkResolvers(t *testing.T) {
	root := manifestFixtureDir(t, "no_manifest_registry_skipped") // registry-only main.tf
	chain, _, err := loader.DefaultResolverChain(root, true)
	if err != nil {
		t.Fatalf("DefaultResolverChain offline: %v", err)
	}
	if chain == nil {
		t.Fatal("expected non-nil chain")
	}
	// Use the chain via LoadProjectWith — registry source should produce no children.
	proj, _, err := loader.LoadProjectWith(root, chain, nil)
	if err != nil {
		t.Fatalf("LoadProjectWith: %v", err)
	}
	if len(proj.Root.Children) != 0 {
		t.Errorf("offline chain + registry source: expected no children, got %v", proj.Root.Children)
	}
}

// TestDefaultResolverChainSeedsManifestWarning: a malformed
// modules.json should surface as a seed FileError, exactly as the
// chain documents.
func TestDefaultResolverChainSeedsManifestWarning(t *testing.T) {
	root := manifestFixtureDir(t, "malformed_manifest_not_fatal")
	_, seed, err := loader.DefaultResolverChain(root, true)
	if err != nil {
		t.Fatalf("DefaultResolverChain: %v", err)
	}
	if len(seed) == 0 {
		t.Fatal("expected seed FileError for malformed modules.json")
	}
	if !filepathContains(seed[0].Path, "modules.json") {
		t.Errorf("seed path = %q, want one mentioning modules.json", seed[0].Path)
	}
}

// TestLoadProjectDefaultsOffline mirrors LoadProject but goes through
// the offline-flagged convenience entry point. Equivalent to the
// hand-built (DefaultResolverChain → LoadProjectWith) flow.
func TestLoadProjectDefaultsOffline(t *testing.T) {
	root := manifestFixtureDir(t, "no_manifest_local_sources_work")
	proj, _, err := loader.LoadProjectDefaults(root, true)
	if err != nil {
		t.Fatalf("LoadProjectDefaults: %v", err)
	}
	if _, ok := proj.Root.Children["child"]; !ok {
		t.Error("local-path child should still resolve under offline + no manifest")
	}
}

// TestLoadProjectDefaultsAbsolutisesRelativePath confirms the helper
// accepts a relative path and resolves it against cwd. Without
// absolutisation, downstream relative-source resolution would
// silently drift.
func TestLoadProjectDefaultsAbsolutisesRelativePath(t *testing.T) {
	abs := manifestFixtureDir(t, "no_manifest_local_sources_work")
	rel, err := filepath.Rel(abs, abs) // "."
	if err != nil {
		t.Fatalf("Rel: %v", err)
	}
	t.Chdir(abs)
	proj, _, err := loader.LoadProjectDefaults(rel, true)
	if err != nil {
		t.Fatalf("LoadProjectDefaults(rel=%q): %v", rel, err)
	}
	if _, ok := proj.Root.Children["child"]; !ok {
		t.Error("expected child to resolve from absolutised relative path")
	}
}

// TestLoadProjectsForDiffOldAndNewLoad: smoke test that the diff-pair
// loader returns two non-nil projects + a non-nil cleanup when given
// a real git repo. Reuses the worktree_test repoSetup helper via
// pkg/git for the underlying repo creation.
func TestLoadProjectsForDiffOldAndNewLoad(t *testing.T) {
	repo := setupRepoWithFixture(t)
	old, newp, cleanup, err := loader.LoadProjectsForDiff(repo, "main", true)
	if err != nil {
		t.Fatalf("LoadProjectsForDiff: %v", err)
	}
	if cleanup == nil {
		t.Error("cleanup should be non-nil")
	}
	defer cleanup()
	if old == nil || newp == nil {
		t.Errorf("old=%v new=%v, want both non-nil", old, newp)
	}
}

// TestLoadProjectsForDiffMissingRefIsError: an unknown base ref
// should produce a non-nil error and a no-op cleanup, NOT a panic.
// Verifies the cleanup func is still safe to defer on the error path.
func TestLoadProjectsForDiffMissingRefIsError(t *testing.T) {
	repo := setupRepoWithFixture(t)
	_, _, cleanup, err := loader.LoadProjectsForDiff(repo, "definitely-not-a-ref", true)
	if err == nil {
		t.Fatal("expected error for unknown ref")
	}
	if cleanup == nil {
		t.Fatal("cleanup should be non-nil even on error")
	}
	cleanup() // must be safe to call
}

// TestLoadForValidateDirRunsCrossValidate: a directory loads as a
// project and runs CrossValidate; a known cross-module problem (parent
// passes unknown arg) must surface in the returned ValidationError list.
func TestLoadForValidateDirRunsCrossValidate(t *testing.T) {
	root := projectFixtureForValidate(t)
	mod, crossErrs, _, err := loader.LoadForValidate(root, true)
	if err != nil {
		t.Fatalf("LoadForValidate: %v", err)
	}
	if mod == nil {
		t.Fatal("expected non-nil module")
	}
	if len(crossErrs) == 0 {
		t.Error("expected at least one cross-module error from parent passing unknown arg")
	}
}

// TestLoadForValidateFileSkipsCrossValidate: a single .tf file path
// loads as just a module — no project tree → no cross-module checks.
// crossErrs must be nil to signal "not applicable".
func TestLoadForValidateFileSkipsCrossValidate(t *testing.T) {
	dir := projectFixtureForValidate(t)
	file := filepath.Join(dir, "main.tf")
	mod, crossErrs, _, err := loader.LoadForValidate(file, true)
	if err != nil {
		t.Fatalf("LoadForValidate(file): %v", err)
	}
	if mod == nil {
		t.Fatal("expected non-nil module")
	}
	if crossErrs != nil {
		t.Errorf("file-mode crossErrs should be nil, got %v", crossErrs)
	}
}

// TestLoadForValidateMissingPathIsError: a non-existent path must
// return a non-nil error rather than panicking on the nil stat result.
func TestLoadForValidateMissingPathIsError(t *testing.T) {
	if _, _, _, err := loader.LoadForValidate(filepath.Join(t.TempDir(), "nope"), true); err == nil {
		t.Error("expected error for missing path")
	}
}

// ---- helpers ----

// setupRepoWithFixture creates a fresh git repo in a temp dir, commits
// a minimal main.tf onto main, and returns the repo path. Used by the
// LoadProjectsForDiff tests; reuses runGit from worktree_test.go.
func setupRepoWithFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	runGit(t, dir, "init", "--quiet", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "main.tf"),
		[]byte("variable \"x\" { type = string }\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", ".")
	runGit(t, dir, "commit", "--quiet", "-m", "init")
	return dir
}

// projectFixtureForValidate builds a temp project where the parent
// passes an unknown arg to a local child — the cross-validate must
// flag it.
func projectFixtureForValidate(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(`module "kid" {
  source     = "./child"
  unexpected = "value"
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	childDir := filepath.Join(dir, "child")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(childDir, "main.tf"), []byte(`variable "x" {
  type    = string
  default = "ok"
}
`), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func filepathContains(path, sub string) bool {
	for i := 0; i+len(sub) <= len(path); i++ {
		if path[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
