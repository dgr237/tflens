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

// projectDir returns the absolute path to testdata/project.
func projectDir() string {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..", "testdata", "project")
	abs, _ := filepath.Abs(root)
	return abs
}

func writeTF(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
		t.Fatalf("writeTF: %v", err)
	}
}

// ---- LoadDir ----

func TestLoadDirEntityCounts(t *testing.T) {
	mod, errs, err := loader.LoadDir(projectDir())
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if len(errs) > 0 {
		t.Fatalf("unexpected parse errors: %v", errs)
	}
	// variables.tf: 2 variables; main.tf: 1 local + 2 module calls; outputs.tf: 2 outputs
	check := func(kind analysis.EntityKind, want int) {
		t.Helper()
		if got := len(mod.Filter(kind)); got != want {
			t.Errorf("%s count: got %d, want %d", kind, got, want)
		}
	}
	check(analysis.KindVariable, 2)
	check(analysis.KindLocal, 1)
	check(analysis.KindModule, 2)
	check(analysis.KindOutput, 2)
}

func TestLoadDirCrossFileDependency(t *testing.T) {
	// local.name_prefix (main.tf) references var.env and var.region (variables.tf).
	mod, _, err := loader.LoadDir(projectDir())
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if !mod.HasDep("local.name_prefix", "variable.env") {
		t.Error("local.name_prefix should depend on variable.env (cross-file ref)")
	}
	if !mod.HasDep("local.name_prefix", "variable.region") {
		t.Error("local.name_prefix should depend on variable.region (cross-file ref)")
	}
}

func TestLoadDirModuleSource(t *testing.T) {
	mod, _, err := loader.LoadDir(projectDir())
	if err != nil {
		t.Fatalf("LoadDir: %v", err)
	}
	if src := mod.ModuleSource("network"); src != "./modules/network" {
		t.Errorf("network source: got %q, want %q", src, "./modules/network")
	}
	if src := mod.ModuleSource("compute"); src != "./modules/compute" {
		t.Errorf("compute source: got %q, want %q", src, "./modules/compute")
	}
}

func TestLoadDirNetworkSubmodule(t *testing.T) {
	netDir := filepath.Join(projectDir(), "modules", "network")
	mod, errs, err := loader.LoadDir(netDir)
	if err != nil {
		t.Fatalf("LoadDir(network): %v", err)
	}
	if len(errs) > 0 {
		t.Fatalf("unexpected parse errors: %v", errs)
	}
	// network module: 2 vars, 1 local, 2 resources, 2 outputs
	check := func(kind analysis.EntityKind, want int) {
		t.Helper()
		if got := len(mod.Filter(kind)); got != want {
			t.Errorf("%s count: got %d, want %d", kind, got, want)
		}
	}
	check(analysis.KindVariable, 2)
	check(analysis.KindLocal, 1)
	check(analysis.KindResource, 2)
	check(analysis.KindOutput, 2)
}

// ---- LoadProject ----

func TestLoadProjectTreeStructure(t *testing.T) {
	proj, errs, err := loader.LoadProject(projectDir())
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if len(errs) > 0 {
		t.Fatalf("unexpected parse errors: %v", errs)
	}
	if proj.Root == nil {
		t.Fatal("root is nil")
	}
	for _, name := range []string{"network", "compute"} {
		if _, ok := proj.Root.Children[name]; !ok {
			t.Errorf("expected child module %q", name)
		}
	}
}

func TestLoadProjectChildEntities(t *testing.T) {
	proj, _, err := loader.LoadProject(projectDir())
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}

	network := proj.Root.Children["network"]
	if got := len(network.Module.Filter(analysis.KindResource)); got != 2 {
		t.Errorf("network resources: got %d, want 2", got)
	}

	compute := proj.Root.Children["compute"]
	if got := len(compute.Module.Filter(analysis.KindResource)); got != 1 {
		t.Errorf("compute resources: got %d, want 1", got)
	}
}

func TestLoadProjectWalkVisitsAllNodes(t *testing.T) {
	proj, _, err := loader.LoadProject(projectDir())
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	count := 0
	proj.Walk(func(_ *loader.ModuleNode) bool {
		count++
		return true
	})
	if count != 3 { // root + network + compute
		t.Errorf("Walk visited %d nodes, want 3", count)
	}
}

func TestLoadProjectWalkCanSkipChildren(t *testing.T) {
	proj, _, err := loader.LoadProject(projectDir())
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	count := 0
	proj.Walk(func(_ *loader.ModuleNode) bool {
		count++
		return false // skip children of every node
	})
	if count != 1 { // only root visited
		t.Errorf("Walk visited %d nodes with skip, want 1", count)
	}
}

func TestLoadProjectNoCycles(t *testing.T) {
	proj, _, err := loader.LoadProject(projectDir())
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	proj.Walk(func(node *loader.ModuleNode) bool {
		if cycles := node.Module.Cycles(); len(cycles) > 0 {
			t.Errorf("cycle in %s: %v", node.Dir, cycles)
		}
		return true
	})
}

func TestLoadProjectChildDependencies(t *testing.T) {
	proj, _, err := loader.LoadProject(projectDir())
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	network := proj.Root.Children["network"]
	if !network.Module.HasDep("resource.aws_subnet.public", "resource.aws_vpc.main") {
		t.Error("network: aws_subnet.public should depend on aws_vpc.main")
	}
	compute := proj.Root.Children["compute"]
	if !compute.Module.HasDep("resource.aws_instance.web", "data.aws_ami.ubuntu") {
		t.Error("compute: aws_instance.web should depend on data.aws_ami.ubuntu")
	}
}

func TestLoadProjectIgnoresRemoteSources(t *testing.T) {
	dir := t.TempDir()
	writeTF(t, dir, "main.tf", `
variable "env" {}
module "remote" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "~> 5.0"
}
`)
	proj, errs, err := loader.LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if len(errs) > 0 {
		t.Fatalf("unexpected errors: %v", errs)
	}
	if len(proj.Root.Children) != 0 {
		t.Errorf("remote source should produce no child nodes, got %d", len(proj.Root.Children))
	}
	if got := len(proj.Root.Module.Filter(analysis.KindModule)); got != 1 {
		t.Errorf("module entity count: got %d, want 1", got)
	}
}

func TestLoadDirParseError(t *testing.T) {
	dir := t.TempDir()
	writeTF(t, dir, "broken.tf", `resource "aws_vpc" "main" { bad syntax ??? }`)
	_, errs, err := loader.LoadDir(dir)
	if err != nil {
		t.Fatalf("unexpected hard error: %v", err)
	}
	if len(errs) == 0 {
		t.Error("expected parse errors for broken file, got none")
	}
	if !strings.Contains(errs[0].Path, "broken.tf") {
		t.Errorf("error path should mention broken.tf, got: %s", errs[0].Path)
	}
}

func TestLoadProjectSharedChildNotLoadedTwice(t *testing.T) {
	// Two sibling modules sharing the same child directory should result in the
	// same *ModuleNode pointer, not two separate loads.
	dir := t.TempDir()
	sharedDir := filepath.Join(dir, "shared")
	if err := os.MkdirAll(sharedDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeTF(t, sharedDir, "main.tf", `variable "x" {}`)
	writeTF(t, dir, "main.tf", `
module "a" { source = "./shared" }
module "b" { source = "./shared" }
`)
	proj, _, err := loader.LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	nodeA := proj.Root.Children["a"]
	nodeB := proj.Root.Children["b"]
	if nodeA == nil || nodeB == nil {
		t.Fatal("expected both child nodes")
	}
	if nodeA != nodeB {
		t.Error("shared module directory should produce the same *ModuleNode (not loaded twice)")
	}
}
