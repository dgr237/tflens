package loader_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/config"
	"github.com/dgr237/tflens/pkg/loader"
)

// loaderProjectCase exercises Loader.Project (or Loader.ForValidate)
// against a static fixture under testdata/manifest/<Fixture>. Custom
// receives the loaded project + any FileError warnings, and the
// fixture root for asserting absolute paths.
type loaderProjectCase struct {
	Name    string
	Fixture string // testdata/manifest subdir
	Custom  func(t *testing.T, p *loader.Project, fileErrs []loader.FileError, root string)
}

func TestLoaderProjectCases(t *testing.T) {
	for _, tc := range loaderProjectCases {
		t.Run(tc.Name, func(t *testing.T) {
			root := manifestFixtureDir(t, tc.Fixture)
			p, fileErrs, err := loader.New(config.Settings{Offline: true}).Project(root)
			if err != nil {
				t.Fatalf("Loader.Project: %v", err)
			}
			tc.Custom(t, p, fileErrs, root)
		})
	}
}

var loaderProjectCases = []loaderProjectCase{
	{
		// Offline mode + a registry-source main.tf: the registry
		// resolver is skipped (not in the chain), so no children
		// resolve. Same expectation as the manifest test's
		// no_manifest_registry_skipped fixture.
		Name:    "offline_skips_registry_sources",
		Fixture: "no_manifest_registry_skipped",
		Custom: func(t *testing.T, p *loader.Project, _ []loader.FileError, _ string) {
			if len(p.Root.Children) != 0 {
				t.Errorf("offline + registry: expected no children, got %v", p.Root.Children)
			}
		},
	},
	{
		// Offline mode still resolves local-path sources via the
		// local resolver in the chain.
		Name:    "offline_local_sources_still_resolve",
		Fixture: "no_manifest_local_sources_work",
		Custom: func(t *testing.T, p *loader.Project, _ []loader.FileError, _ string) {
			if _, ok := p.Root.Children["child"]; !ok {
				t.Error("local-path child should resolve under offline mode")
			}
		},
	},
	{
		// A malformed .terraform/modules/modules.json surfaces as a
		// seed FileError — exercises the chain's seed-warning path
		// without a direct call to defaultResolverChain.
		Name:    "manifest_warning_seeded_as_file_error",
		Fixture: "malformed_manifest_not_fatal",
		Custom: func(t *testing.T, _ *loader.Project, fileErrs []loader.FileError, _ string) {
			if len(fileErrs) == 0 {
				t.Fatal("expected seed FileError for malformed modules.json")
			}
			if !strings.Contains(fileErrs[0].Path, "modules.json") {
				t.Errorf("seed path = %q, want one mentioning modules.json", fileErrs[0].Path)
			}
		},
	},
}

// TestLoaderProjectAbsolutisesRelativePath checks the convenience
// behaviour separately because it needs t.Chdir, which doesn't fit
// the table shape cleanly.
func TestLoaderProjectAbsolutisesRelativePath(t *testing.T) {
	abs := manifestFixtureDir(t, "no_manifest_local_sources_work")
	t.Chdir(abs)
	p, _, err := loader.New(config.Settings{Offline: true}).Project(".")
	if err != nil {
		t.Fatalf("Loader.Project(.): %v", err)
	}
	if _, ok := p.Root.Children["child"]; !ok {
		t.Error("expected child to resolve from absolutised relative path")
	}
}

// TestLoaderForValidateCases exercises Loader.ForValidate's three
// dispatch paths (dir → cross-validate runs; file → skipped; missing
// → error). Each case wraps its own assertion to avoid leaking the
// analysis types into a shared case-struct signature.
func TestLoaderForValidateCases(t *testing.T) {
	cases := []struct {
		Name   string
		Path   func(t *testing.T) string
		Custom func(t *testing.T, p string)
	}{
		{
			// A directory with a parent passing an unknown arg to a
			// local child: ForValidate runs CrossValidate over the
			// project and surfaces the error.
			Name: "dir_runs_cross_validate",
			Path: projectFixtureForValidate,
			Custom: func(t *testing.T, p string) {
				mod, crossErrs, _, err := loader.New(config.Settings{Offline: true}).ForValidate(p)
				if err != nil {
					t.Fatalf("ForValidate: %v", err)
				}
				if mod == nil {
					t.Fatal("expected non-nil module")
				}
				if len(crossErrs) == 0 {
					t.Error("expected cross-module error from parent passing unknown arg")
				}
			},
		},
		{
			// A single .tf file path skips the project tree and
			// therefore CrossValidate. crossErrs MUST be nil to
			// signal "not applicable", not an empty slice.
			Name: "file_skips_cross_validate",
			Path: func(t *testing.T) string {
				return filepath.Join(projectFixtureForValidate(t), "main.tf")
			},
			Custom: func(t *testing.T, p string) {
				mod, crossErrs, _, err := loader.New(config.Settings{Offline: true}).ForValidate(p)
				if err != nil {
					t.Fatalf("ForValidate(file): %v", err)
				}
				if mod == nil {
					t.Fatal("expected non-nil module")
				}
				if crossErrs != nil {
					t.Errorf("file-mode crossErrs should be nil, got %v", crossErrs)
				}
			},
		},
		{
			// A non-existent path returns a non-nil error rather
			// than panicking on the nil stat result.
			Name: "missing_path_is_error",
			Path: func(t *testing.T) string { return filepath.Join(t.TempDir(), "nope") },
			Custom: func(t *testing.T, p string) {
				if _, _, _, err := loader.New(config.Settings{Offline: true}).ForValidate(p); err == nil {
					t.Error("expected error for missing path")
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			tc.Custom(t, tc.Path(t))
		})
	}
}

// loaderProjectsForDiffCase pairs a repo-builder with assertions on
// Loader.ProjectsForDiff. Each case builds a fresh git repo (or just
// a tempdir for the not-a-repo case) so the worktree-staging path is
// exercised end-to-end.
type loaderProjectsForDiffCase struct {
	Name    string
	BaseRef string
	Setup   func(t *testing.T) string
	Custom  func(t *testing.T, old, newp *loader.Project, cleanup func(), err error)
}

func TestLoaderProjectsForDiffCases(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	for _, tc := range loaderProjectsForDiffCases {
		t.Run(tc.Name, func(t *testing.T) {
			path := tc.Setup(t)
			old, newp, cleanup, err := loader.New(config.Settings{Offline: true}).
				ProjectsForDiff(path, tc.BaseRef)
			tc.Custom(t, old, newp, cleanup, err)
		})
	}
}

var loaderProjectsForDiffCases = []loaderProjectsForDiffCase{
	{
		// Smoke test: a fresh repo with a single commit on `main`
		// loads cleanly when comparing main↔main. Both projects are
		// non-nil; cleanup is callable.
		Name:    "smoke_clean_load",
		BaseRef: "main",
		Setup:   setupRepoWithFixture,
		Custom: func(t *testing.T, old, newp *loader.Project, cleanup func(), err error) {
			if err != nil {
				t.Fatalf("ProjectsForDiff: %v", err)
			}
			if cleanup == nil {
				t.Error("cleanup should be non-nil on success")
			}
			defer cleanup()
			if old == nil || newp == nil {
				t.Errorf("old=%v new=%v, want both non-nil", old, newp)
			}
		},
	},
	{
		// An unknown base ref returns a non-nil error and a no-op
		// cleanup (must still be safe to defer). Exercises the
		// prepareWorktree → git.RevParse error path.
		Name:    "unknown_ref_is_error",
		BaseRef: "definitely-not-a-ref",
		Setup:   setupRepoWithFixture,
		Custom: func(t *testing.T, _, _ *loader.Project, cleanup func(), err error) {
			if err == nil {
				t.Fatal("expected error for unknown ref")
			}
			if cleanup == nil {
				t.Fatal("cleanup should be non-nil even on error")
			}
			cleanup() // must be safe to call
		},
	},
	{
		// A path that isn't inside any git repo errors out with a
		// "not inside a git repository" message. Exercises the
		// prepareWorktree → git.TopLevel error path.
		Name:    "outside_repo_is_error",
		BaseRef: "main",
		Setup:   func(t *testing.T) string { return t.TempDir() },
		Custom: func(t *testing.T, _, _ *loader.Project, cleanup func(), err error) {
			if err == nil {
				t.Error("expected error for non-repo path")
			}
			if cleanup == nil {
				t.Fatal("cleanup should be non-nil even on error")
			}
			cleanup()
		},
	},
}

// ---- helpers ----

// setupRepoWithFixture creates a fresh git repo in a temp dir, commits
// a minimal main.tf onto main, and returns the repo path. Used by the
// ProjectsForDiff cases.
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
// flag it. Reused by TestLoaderForValidateCases.
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
