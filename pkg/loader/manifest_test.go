package loader_test

import (
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/loader"
)

// manifestFixtureDir returns the absolute path to
// pkg/loader/testdata/manifest/<case>. Each fixture mirrors a
// post-`terraform init` workspace: a root main.tf, optional local
// child directories, and an optional .terraform/modules/modules.json
// that maps registry / nested keys to on-disk paths.
func manifestFixtureDir(t *testing.T, name string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	abs, err := filepath.Abs(filepath.Join(filepath.Dir(file), "testdata", "manifest", name))
	if err != nil {
		t.Fatalf("resolving fixture %s: %v", name, err)
	}
	return abs
}

// manifestCase pairs a fixture with an assertion over the loaded
// project, the FileError list (used by the malformed-manifest case),
// and the absolute fixture root (so cases can compare child Dir
// values against expected absolute paths).
type manifestCase struct {
	Name   string
	Custom func(t *testing.T, p *loader.Project, fileErrs []loader.FileError, root string)
}

func TestManifestCases(t *testing.T) {
	for _, tc := range manifestCases {
		t.Run(tc.Name, func(t *testing.T) {
			root := manifestFixtureDir(t, tc.Name)
			p, fileErrs, err := loader.LoadProject(root)
			if err != nil {
				t.Fatalf("LoadProject: %v", err)
			}
			tc.Custom(t, p, fileErrs, root)
		})
	}
}

var manifestCases = []manifestCase{
	{
		// Simulate a post-`terraform init` workspace: parent references
		// a registry module, modules.json maps it to a local directory
		// holding the downloaded source.
		Name: "resolves_registry_source",
		Custom: func(t *testing.T, p *loader.Project, _ []loader.FileError, root string) {
			vpc, ok := p.Root.Children["vpc"]
			if !ok {
				t.Fatalf("expected child 'vpc' loaded via manifest, got: %v", p.Root.Children)
			}
			want := filepath.Join(root, ".terraform", "modules", "vpc")
			if vpc.Dir != want {
				t.Errorf("child Dir = %q, want %q", vpc.Dir, want)
			}
			// Parent passes cidr (required), region has a default — no errors.
			if errs := loader.CrossValidate(p); len(errs) != 0 {
				t.Errorf("expected no cross-validate errors, got: %v", errs)
			}
		},
	},
	{
		// Parent omits cidr; child declares it as required.
		Name: "detects_missing_required_input",
		Custom: func(t *testing.T, p *loader.Project, _ []loader.FileError, _ string) {
			errs := loader.CrossValidate(p)
			for _, e := range errs {
				if e.EntityID == "module.vpc" && strings.Contains(e.Msg, "cidr") &&
					strings.Contains(e.Msg, "required input") {
					return
				}
			}
			t.Errorf("expected required-input error against registry module, got: %v", errs)
		},
	},
	{
		// Parent passes cidr=42 (number); child declares string.
		Name: "detects_type_mismatch",
		Custom: func(t *testing.T, p *loader.Project, _ []loader.FileError, _ string) {
			errs := loader.CrossValidate(p)
			for _, e := range errs {
				if e.EntityID == "module.vpc" && strings.Contains(e.Msg, "number") &&
					strings.Contains(e.Msg, "string") {
					return
				}
			}
			t.Errorf("expected type mismatch against registry module, got: %v", errs)
		},
	},
	{
		// Root → vpc (registry) → sg (submodule of vpc). Manifest Key
		// for the grandchild is "vpc.sg".
		Name: "resolves_nested_dotted_keys",
		Custom: func(t *testing.T, p *loader.Project, _ []loader.FileError, root string) {
			vpc, ok := p.Root.Children["vpc"]
			if !ok {
				t.Fatal("expected vpc child")
			}
			sg, ok := vpc.Children["sg"]
			if !ok {
				t.Fatal("expected vpc.sg grandchild")
			}
			want := filepath.Join(root, ".terraform", "modules", "vpc", "submodules", "sg")
			if sg.Dir != want {
				t.Errorf("sg.Dir = %q, want %q", sg.Dir, want)
			}
			if errs := loader.CrossValidate(p); len(errs) != 0 {
				t.Errorf("expected no cross errors in well-formed nested project, got: %v", errs)
			}
		},
	},
	{
		// Without a manifest, local path sources resolve as before.
		Name: "no_manifest_local_sources_work",
		Custom: func(t *testing.T, p *loader.Project, _ []loader.FileError, _ string) {
			if _, ok := p.Root.Children["child"]; !ok {
				t.Error("local-path child should still be loaded without a manifest")
			}
		},
	},
	{
		// No manifest + a registry source → not loaded, but not an error.
		Name: "no_manifest_registry_skipped",
		Custom: func(t *testing.T, p *loader.Project, _ []loader.FileError, _ string) {
			if len(p.Root.Children) != 0 {
				t.Errorf("no manifest + registry source should produce no children, got: %v",
					p.Root.Children)
			}
			if errs := loader.CrossValidate(p); len(errs) != 0 {
				t.Errorf("no manifest → no cross errors on remote module, got: %v", errs)
			}
		},
	},
	{
		// Manifest mentions only "other"; the parent's "local" child
		// should still resolve via the local-source fallback.
		Name: "partial_coverage_falls_back_to_local",
		Custom: func(t *testing.T, p *loader.Project, _ []loader.FileError, _ string) {
			if _, ok := p.Root.Children["local"]; !ok {
				t.Errorf("local child should resolve via fallback when manifest omits it, got: %v",
					p.Root.Children)
			}
		},
	},
	{
		// A broken manifest surfaces as a FileError warning but does
		// not block the rest of the project from loading.
		Name: "malformed_manifest_not_fatal",
		Custom: func(t *testing.T, p *loader.Project, fileErrs []loader.FileError, _ string) {
			if p == nil || p.Root == nil {
				t.Fatal("project should still load")
			}
			for _, fe := range fileErrs {
				if strings.Contains(fe.Path, "modules.json") {
					return
				}
			}
			t.Errorf("malformed manifest should be reported as a parse warning, got: %v", fileErrs)
		},
	},
}
