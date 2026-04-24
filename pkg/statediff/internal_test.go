package statediff

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/tfstate"
)

// loadModulesOrFail returns the walked-modules map for a single-file
// fixture, mirroring what Analyze does internally.
func loadModulesOrFail(t *testing.T, src string) map[string]*loader.ModuleNode {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	proj, _, err := loader.LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	return walkAllModules(proj)
}

// TestDetectStateOrphans: instances tracked in state but with no
// declaration in the new tree show up as orphans. Declared
// instances do not.
func TestDetectStateOrphans(t *testing.T) {
	state := &tfstate.State{
		Resources: []tfstate.Resource{
			{
				Module: "", Mode: tfstate.ModeManaged, Type: "aws_vpc", Name: "main",
				Instances: []tfstate.Instance{{}},
			},
			{
				Module: "", Mode: tfstate.ModeManaged, Type: "aws_eip", Name: "orphan",
				Instances: []tfstate.Instance{{}},
			},
		},
	}
	// New tree declares aws_vpc.main but not aws_eip.orphan.
	newMods := loadModulesOrFail(t, ``+
		`resource "aws_vpc" "main" { cidr_block = "10.0.0.0/16" }`)

	got := detectStateOrphans(state, newMods)
	if len(got) != 1 {
		t.Fatalf("got %d orphans, want 1: %v", len(got), got)
	}
	if got[0] != "aws_eip.orphan" {
		t.Errorf("orphan address = %q, want aws_eip.orphan", got[0])
	}
}

func TestDetectStateOrphansSortedDeterministically(t *testing.T) {
	state := &tfstate.State{
		Resources: []tfstate.Resource{
			{Module: "", Mode: tfstate.ModeManaged, Type: "aws_vpc", Name: "z", Instances: []tfstate.Instance{{}}},
			{Module: "", Mode: tfstate.ModeManaged, Type: "aws_vpc", Name: "a", Instances: []tfstate.Instance{{}}},
		},
	}
	newMods := loadModulesOrFail(t, `# nothing declared`)
	got := detectStateOrphans(state, newMods)
	if len(got) != 2 || got[0] != "aws_vpc.a" || got[1] != "aws_vpc.z" {
		t.Errorf("not sorted: %v", got)
	}
}

// TestDetectStateOrphansIgnoresDataSources: data sources behave
// the same as managed resources for the orphan calculation.
func TestDetectStateOrphansSeparatesManagedAndData(t *testing.T) {
	state := &tfstate.State{
		Resources: []tfstate.Resource{
			// In state as managed; declared as data → still orphan.
			{Module: "", Mode: tfstate.ModeManaged, Type: "aws_vpc", Name: "x",
				Instances: []tfstate.Instance{{}}},
		},
	}
	newMods := loadModulesOrFail(t, `data "aws_vpc" "x" { id = "vpc-1" }`)
	got := detectStateOrphans(state, newMods)
	if len(got) != 1 || got[0] != "aws_vpc.x" {
		t.Errorf("managed-vs-data should not match for orphan check; got %v", got)
	}
}

// TestMatchingStateInstancesReturnsAllForResource: when the state
// has instances of a resource, matchingStateInstances returns their
// full addresses sorted. With no state at all, returns nil.
func TestMatchingStateInstancesReturnsAllForResource(t *testing.T) {
	mods := loadModulesOrFail(t, `
resource "aws_vpc" "main" {
  for_each   = toset(["us-east-1", "us-west-2"])
  cidr_block = "10.0.0.0/16"
}
`)
	state := &tfstate.State{
		Resources: []tfstate.Resource{
			{
				Module: "", Mode: tfstate.ModeManaged, Type: "aws_vpc", Name: "main",
				Instances: []tfstate.Instance{
					{IndexKey: "us-east-1"},
					{IndexKey: "us-west-2"},
				},
			},
		},
	}

	rootMod := mods[""].Module
	var entity, ok = rootMod.EntityByID("resource.aws_vpc.main")
	if !ok {
		t.Fatal("expected to find resource.aws_vpc.main in test fixture")
	}

	got := matchingStateInstances(state, "", entity)
	if len(got) != 2 {
		t.Fatalf("got %d instances, want 2: %v", len(got), got)
	}
	// Sorted alphabetically.
	if got[0] != `aws_vpc.main["us-east-1"]` {
		t.Errorf("[0] = %q", got[0])
	}
	if got[1] != `aws_vpc.main["us-west-2"]` {
		t.Errorf("[1] = %q", got[1])
	}
}

func TestMatchingStateInstancesReturnsNilForUnknownResource(t *testing.T) {
	mods := loadModulesOrFail(t, `resource "aws_vpc" "main" {}`)
	state := &tfstate.State{} // empty
	rootMod := mods[""].Module
	entity, _ := rootMod.EntityByID("resource.aws_vpc.main")
	if got := matchingStateInstances(state, "", entity); got != nil {
		t.Errorf("expected nil for resource not in state, got %v", got)
	}
}

// TestTransitivelyDependsOnDirect covers the trivial path: from
// directly depends on to (one edge in the graph).
func TestTransitivelyDependsOnDirect(t *testing.T) {
	mods := loadModulesOrFail(t, `
locals {
  a = "value"
}
resource "aws_vpc" "main" {
  cidr_block = local.a
}
`)
	mod := mods[""].Module
	if !transitivelyDependsOn(mod, "resource.aws_vpc.main", "local.a") {
		t.Error("resource.aws_vpc.main should directly depend on local.a")
	}
}

// TestTransitivelyDependsOnIndirect: from depends on intermediate,
// intermediate depends on to. The BFS walks both edges.
func TestTransitivelyDependsOnIndirect(t *testing.T) {
	mods := loadModulesOrFail(t, `
variable "x" { type = string }
locals {
  intermediate = var.x
}
resource "aws_vpc" "main" {
  cidr_block = local.intermediate
}
`)
	mod := mods[""].Module
	if !transitivelyDependsOn(mod, "resource.aws_vpc.main", "variable.x") {
		t.Error("resource.aws_vpc.main should transitively depend on variable.x via local.intermediate")
	}
}

func TestTransitivelyDependsOnUnreachable(t *testing.T) {
	mods := loadModulesOrFail(t, `
locals {
  a = "x"
  b = "y"
}
`)
	mod := mods[""].Module
	if transitivelyDependsOn(mod, "local.a", "local.b") {
		t.Error("local.a and local.b are independent; should not depend transitively")
	}
}

func TestTransitivelyDependsOnSelfReturnsTrue(t *testing.T) {
	mods := loadModulesOrFail(t, `locals { a = "x" }`)
	mod := mods[""].Module
	if !transitivelyDependsOn(mod, "local.a", "local.a") {
		t.Error("self-dependency should be true")
	}
}
