package loader_test

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/loader"
)

// projectDir returns the absolute path to testdata/project — the
// canonical "real Terraform project" fixture shared across LoadDir
// and LoadProject cases.
func projectDir() string {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "testdata", "project")
	abs, _ := filepath.Abs(root)
	return abs
}

// loaderFixtureDir returns the absolute path to
// pkg/loader/testdata/loader/<name> — the per-case fixtures used
// by single-purpose load tests (parse error, remote source skip,
// shared-child de-dup).
func loaderFixtureDir(t *testing.T, name string) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	abs, err := filepath.Abs(filepath.Join(filepath.Dir(file), "testdata", "loader", name))
	if err != nil {
		t.Fatalf("resolving fixture %s: %v", name, err)
	}
	return abs
}

func writeTF(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("writeTF: %v", err)
	}
}

// ---- LoadDir ----

// loadDirCase pairs a fixture directory with assertions on the loaded
// module. Dir is the path to load — typically projectDir() for the
// canonical project, or loaderFixtureDir(t, ...) for a per-case
// fixture.
type loadDirCase struct {
	Name   string
	Dir    func(t *testing.T) string
	Custom func(t *testing.T, m *analysis.Module, errs []loader.FileError, err error)
}

func TestLoadDirCases(t *testing.T) {
	for _, tc := range loadDirCases {
		t.Run(tc.Name, func(t *testing.T) {
			m, errs, err := loader.LoadDir(tc.Dir(t))
			tc.Custom(t, m, errs, err)
		})
	}
}

var loadDirCases = []loadDirCase{
	{
		Name: "entity_counts",
		Dir:  func(_ *testing.T) string { return projectDir() },
		Custom: func(t *testing.T, m *analysis.Module, errs []loader.FileError, err error) {
			requireOK(t, errs, err)
			// variables.tf: 2 variables; main.tf: 1 local + 2 module calls;
			// outputs.tf: 2 outputs.
			expectKindCount(t, m, analysis.KindVariable, 2)
			expectKindCount(t, m, analysis.KindLocal, 1)
			expectKindCount(t, m, analysis.KindModule, 2)
			expectKindCount(t, m, analysis.KindOutput, 2)
		},
	},
	{
		Name: "cross_file_dependency",
		Dir:  func(_ *testing.T) string { return projectDir() },
		Custom: func(t *testing.T, m *analysis.Module, _ []loader.FileError, err error) {
			if err != nil {
				t.Fatalf("LoadDir: %v", err)
			}
			// local.name_prefix in main.tf references vars defined in variables.tf.
			if !m.HasDep("local.name_prefix", "variable.env") {
				t.Error("local.name_prefix should depend on variable.env (cross-file ref)")
			}
			if !m.HasDep("local.name_prefix", "variable.region") {
				t.Error("local.name_prefix should depend on variable.region (cross-file ref)")
			}
		},
	},
	{
		Name: "module_sources",
		Dir:  func(_ *testing.T) string { return projectDir() },
		Custom: func(t *testing.T, m *analysis.Module, _ []loader.FileError, err error) {
			if err != nil {
				t.Fatalf("LoadDir: %v", err)
			}
			if src := m.ModuleSource("network"); src != "./modules/network" {
				t.Errorf("network source: got %q, want %q", src, "./modules/network")
			}
			if src := m.ModuleSource("compute"); src != "./modules/compute" {
				t.Errorf("compute source: got %q, want %q", src, "./modules/compute")
			}
		},
	},
	{
		Name: "network_submodule",
		Dir:  func(_ *testing.T) string { return filepath.Join(projectDir(), "modules", "network") },
		Custom: func(t *testing.T, m *analysis.Module, errs []loader.FileError, err error) {
			requireOK(t, errs, err)
			expectKindCount(t, m, analysis.KindVariable, 2)
			expectKindCount(t, m, analysis.KindLocal, 1)
			expectKindCount(t, m, analysis.KindResource, 2)
			expectKindCount(t, m, analysis.KindOutput, 2)
		},
	},
	{
		Name: "parse_error",
		Dir:  func(t *testing.T) string { return loaderFixtureDir(t, "parse_error") },
		Custom: func(t *testing.T, _ *analysis.Module, errs []loader.FileError, err error) {
			if err != nil {
				t.Fatalf("unexpected hard error: %v", err)
			}
			if len(errs) == 0 {
				t.Fatal("expected parse errors for broken file, got none")
			}
			if !strings.Contains(errs[0].Path, "broken.tf") {
				t.Errorf("error path should mention broken.tf, got: %s", errs[0].Path)
			}
		},
	},
}

// ---- LoadProject ----

// loadProjectCase pairs a fixture directory with assertions on the
// loaded project tree. Same shape as loadDirCase but the result type
// is a Project instead of a Module.
type loadProjectCase struct {
	Name   string
	Dir    func(t *testing.T) string
	Custom func(t *testing.T, p *loader.Project, errs []loader.FileError, err error)
}

func TestLoadProjectCases(t *testing.T) {
	for _, tc := range loadProjectCases {
		t.Run(tc.Name, func(t *testing.T) {
			p, errs, err := loader.LoadProject(tc.Dir(t))
			tc.Custom(t, p, errs, err)
		})
	}
}

var loadProjectCases = []loadProjectCase{
	{
		Name: "tree_structure",
		Dir:  func(_ *testing.T) string { return projectDir() },
		Custom: func(t *testing.T, p *loader.Project, errs []loader.FileError, err error) {
			requireOK(t, errs, err)
			if p.Root == nil {
				t.Fatal("root is nil")
			}
			for _, name := range []string{"network", "compute"} {
				if _, ok := p.Root.Children[name]; !ok {
					t.Errorf("expected child module %q", name)
				}
			}
		},
	},
	{
		Name: "child_entities",
		Dir:  func(_ *testing.T) string { return projectDir() },
		Custom: func(t *testing.T, p *loader.Project, _ []loader.FileError, err error) {
			if err != nil {
				t.Fatalf("LoadProject: %v", err)
			}
			if got := len(p.Root.Children["network"].Module.Filter(analysis.KindResource)); got != 2 {
				t.Errorf("network resources: got %d, want 2", got)
			}
			if got := len(p.Root.Children["compute"].Module.Filter(analysis.KindResource)); got != 1 {
				t.Errorf("compute resources: got %d, want 1", got)
			}
		},
	},
	{
		Name: "walk_visits_all_nodes",
		Dir:  func(_ *testing.T) string { return projectDir() },
		Custom: func(t *testing.T, p *loader.Project, _ []loader.FileError, err error) {
			if err != nil {
				t.Fatalf("LoadProject: %v", err)
			}
			count := 0
			p.Walk(func(_ *loader.ModuleNode) bool {
				count++
				return true
			})
			if count != 3 { // root + network + compute
				t.Errorf("Walk visited %d nodes, want 3", count)
			}
		},
	},
	{
		Name: "walk_can_skip_children",
		Dir:  func(_ *testing.T) string { return projectDir() },
		Custom: func(t *testing.T, p *loader.Project, _ []loader.FileError, err error) {
			if err != nil {
				t.Fatalf("LoadProject: %v", err)
			}
			count := 0
			p.Walk(func(_ *loader.ModuleNode) bool {
				count++
				return false // skip children of every node
			})
			if count != 1 { // only root visited
				t.Errorf("Walk visited %d nodes with skip, want 1", count)
			}
		},
	},
	{
		Name: "no_cycles",
		Dir:  func(_ *testing.T) string { return projectDir() },
		Custom: func(t *testing.T, p *loader.Project, _ []loader.FileError, err error) {
			if err != nil {
				t.Fatalf("LoadProject: %v", err)
			}
			p.Walk(func(node *loader.ModuleNode) bool {
				if cycles := node.Module.Cycles(); len(cycles) > 0 {
					t.Errorf("cycle in %s: %v", node.Dir, cycles)
				}
				return true
			})
		},
	},
	{
		Name: "child_dependencies",
		Dir:  func(_ *testing.T) string { return projectDir() },
		Custom: func(t *testing.T, p *loader.Project, _ []loader.FileError, err error) {
			if err != nil {
				t.Fatalf("LoadProject: %v", err)
			}
			if !p.Root.Children["network"].Module.HasDep("resource.aws_subnet.public", "resource.aws_vpc.main") {
				t.Error("network: aws_subnet.public should depend on aws_vpc.main")
			}
			if !p.Root.Children["compute"].Module.HasDep("resource.aws_instance.web", "data.aws_ami.ubuntu") {
				t.Error("compute: aws_instance.web should depend on data.aws_ami.ubuntu")
			}
		},
	},
	{
		Name: "ignores_remote_sources",
		Dir:  func(t *testing.T) string { return loaderFixtureDir(t, "ignores_remote_sources") },
		Custom: func(t *testing.T, p *loader.Project, errs []loader.FileError, err error) {
			requireOK(t, errs, err)
			if len(p.Root.Children) != 0 {
				t.Errorf("remote source should produce no child nodes, got %d", len(p.Root.Children))
			}
			if got := len(p.Root.Module.Filter(analysis.KindModule)); got != 1 {
				t.Errorf("module entity count: got %d, want 1", got)
			}
		},
	},
	{
		Name: "shared_child_not_loaded_twice",
		// Two sibling modules sharing a child directory must produce the
		// same *ModuleNode pointer, not two independent loads.
		Dir: func(t *testing.T) string { return loaderFixtureDir(t, "shared_child") },
		Custom: func(t *testing.T, p *loader.Project, _ []loader.FileError, err error) {
			if err != nil {
				t.Fatalf("LoadProject: %v", err)
			}
			a, b := p.Root.Children["a"], p.Root.Children["b"]
			if a == nil || b == nil {
				t.Fatal("expected both child nodes")
			}
			if a != b {
				t.Error("shared module directory should produce the same *ModuleNode (not loaded twice)")
			}
		},
	},
}

// ---- shared assertions ----

// requireOK asserts no hard error and no parse errors. Used by cases
// where any failure invalidates downstream assertions.
func requireOK(t *testing.T, errs []loader.FileError, err error) {
	t.Helper()
	if err != nil {
		t.Fatalf("loader: %v", err)
	}
	if len(errs) > 0 {
		t.Fatalf("unexpected parse errors: %v", errs)
	}
}

// expectKindCount asserts that m has exactly want entities of the given kind.
func expectKindCount(t *testing.T, m *analysis.Module, kind analysis.EntityKind, want int) {
	t.Helper()
	if got := len(m.Filter(kind)); got != want {
		t.Errorf("%s count: got %d, want %d", kind, got, want)
	}
}
