package statediff_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/statediff"
)

func TestResourceRefAddress(t *testing.T) {
	cases := []struct {
		r    statediff.ResourceRef
		want string
	}{
		{statediff.ResourceRef{Type: "aws_vpc", Name: "main"}, "aws_vpc.main"},
		{statediff.ResourceRef{Module: "module.x", Type: "aws_vpc", Name: "main"}, "module.x.aws_vpc.main"},
	}
	for _, c := range cases {
		if got := c.r.Address(); got != c.want {
			t.Errorf("%+v: Address() = %q, want %q", c.r, got, c.want)
		}
	}
}

func TestRenamePairAddresses(t *testing.T) {
	r := statediff.RenamePair{
		Module: "module.vpc",
		From:   "resource.aws_subnet.old",
		To:     "resource.aws_subnet.new",
	}
	if got := r.FromAddress(); got != "module.vpc.aws_subnet.old" {
		t.Errorf("FromAddress = %q", got)
	}
	if got := r.ToAddress(); got != "module.vpc.aws_subnet.new" {
		t.Errorf("ToAddress = %q", got)
	}
}

func TestAffectedResourceAddress(t *testing.T) {
	cases := []struct {
		a    statediff.AffectedResource
		want string
	}{
		{statediff.AffectedResource{Type: "aws_instance", Name: "web"}, "aws_instance.web"},
		{statediff.AffectedResource{Module: "module.app", Type: "aws_instance", Name: "web"}, "module.app.aws_instance.web"},
	}
	for _, c := range cases {
		if got := c.a.Address(); got != c.want {
			t.Errorf("Address() = %q, want %q", got, c.want)
		}
	}
}

func TestResultFlaggedCount(t *testing.T) {
	r := statediff.Result{
		AddedResources:   make([]statediff.ResourceRef, 2),
		RemovedResources: make([]statediff.ResourceRef, 1),
		RenamedResources: make([]statediff.RenamePair, 5), // not counted
		SensitiveChanges: make([]statediff.SensitiveChange, 3),
		StateOrphans:     []string{"a", "b", "c", "d"}, // not counted
	}
	if got := r.FlaggedCount(); got != 6 {
		t.Errorf("FlaggedCount = %d, want 6 (2+1+3, renames + orphans excluded)", got)
	}
}

// TestAnalyzeNilProjects: Analyze must be safe with nil inputs.
func TestAnalyzeNilProjects(t *testing.T) {
	r := statediff.Analyze(nil, nil, nil)
	if got := r.FlaggedCount(); got != 0 {
		t.Errorf("nil/nil: flagged = %d, want 0", got)
	}
}

// TestAnalyzeAddedResource: a resource that exists only on the new
// side shows up under AddedResources and counts toward the gate.
func TestAnalyzeAddedResource(t *testing.T) {
	old := loadProj(t, `# empty`)
	new := loadProj(t, `
resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}
`)
	r := statediff.Analyze(old, new, nil)
	if len(r.AddedResources) != 1 {
		t.Fatalf("AddedResources = %v, want 1", r.AddedResources)
	}
	if a := r.AddedResources[0]; a.Type != "aws_vpc" || a.Name != "main" {
		t.Errorf("AddedResources[0] = %+v, want aws_vpc.main", a)
	}
	if r.FlaggedCount() == 0 {
		t.Error("added resource should bump FlaggedCount")
	}
}

// TestAnalyzeRemovedResource: present in old, gone from new.
func TestAnalyzeRemovedResource(t *testing.T) {
	old := loadProj(t, `
resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}
`)
	new := loadProj(t, `# empty`)
	r := statediff.Analyze(old, new, nil)
	if len(r.RemovedResources) != 1 {
		t.Fatalf("RemovedResources = %v, want 1", r.RemovedResources)
	}
}

// TestAnalyzeRenameViaMovedBlockSuppressesAddRemove: a moved block
// covering an old name → new name pair makes neither side count as
// added/removed, AND it's listed under RenamedResources (which
// doesn't gate CI).
func TestAnalyzeRenameViaMovedBlock(t *testing.T) {
	old := loadProj(t, `
resource "aws_vpc" "old_name" {
  cidr_block = "10.0.0.0/16"
}
`)
	new := loadProj(t, `
resource "aws_vpc" "new_name" {
  cidr_block = "10.0.0.0/16"
}

moved {
  from = aws_vpc.old_name
  to   = aws_vpc.new_name
}
`)
	r := statediff.Analyze(old, new, nil)
	if len(r.AddedResources) != 0 {
		t.Errorf("rename should suppress added: %v", r.AddedResources)
	}
	if len(r.RemovedResources) != 0 {
		t.Errorf("rename should suppress removed: %v", r.RemovedResources)
	}
	if len(r.RenamedResources) != 1 {
		t.Fatalf("RenamedResources = %v, want 1", r.RenamedResources)
	}
	if r.FlaggedCount() != 0 {
		t.Errorf("rename alone should not gate CI; FlaggedCount = %d", r.FlaggedCount())
	}
}

// TestAnalyzeSensitiveLocalReachesCount: editing a local that the
// resource's count expression depends on must be flagged.
func TestAnalyzeSensitiveLocalReachesCount(t *testing.T) {
	old := loadProj(t, `
locals {
  enabled = 1
}
resource "aws_vpc" "main" {
  count       = local.enabled
  cidr_block  = "10.0.0.0/16"
}
`)
	new := loadProj(t, `
locals {
  enabled = 0
}
resource "aws_vpc" "main" {
  count       = local.enabled
  cidr_block  = "10.0.0.0/16"
}
`)
	r := statediff.Analyze(old, new, nil)
	if len(r.SensitiveChanges) != 1 {
		t.Fatalf("SensitiveChanges = %v, want 1", r.SensitiveChanges)
	}
	c := r.SensitiveChanges[0]
	if c.Kind != "local" || c.Name != "enabled" {
		t.Errorf("change = %+v, want local.enabled", c)
	}
	if c.OldValue != "1" || c.NewValue != "0" {
		t.Errorf("values: old=%q new=%q, want 1/0", c.OldValue, c.NewValue)
	}
	if len(c.AffectedResources) != 1 {
		t.Fatalf("affected = %v, want 1", c.AffectedResources)
	}
	a := c.AffectedResources[0]
	if a.Type != "aws_vpc" || a.Name != "main" || a.MetaArg != "count" {
		t.Errorf("affected[0] = %+v", a)
	}
}

// TestAnalyzeSensitiveLocalNoReachIsSilent: a local that no
// count/for_each touches is just an internal value change — not
// flagged.
func TestAnalyzeSensitiveLocalNoReachIsSilent(t *testing.T) {
	old := loadProj(t, `
locals { tag = "old" }
resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}
`)
	new := loadProj(t, `
locals { tag = "new" }
resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}
`)
	r := statediff.Analyze(old, new, nil)
	if len(r.SensitiveChanges) != 0 {
		t.Errorf("local edit not reaching count/for_each shouldn't flag; got %v", r.SensitiveChanges)
	}
}

// TestAnalyzeForEachSet: same logic for for_each — a changed local
// that the for_each set depends on flags the resource.
func TestAnalyzeForEachSet(t *testing.T) {
	old := loadProj(t, `
locals { regions = toset(["us-east-1", "us-west-2"]) }
resource "aws_vpc" "main" {
  for_each   = local.regions
  cidr_block = "10.0.0.0/16"
}
`)
	new := loadProj(t, `
locals { regions = toset(["us-east-1"]) }
resource "aws_vpc" "main" {
  for_each   = local.regions
  cidr_block = "10.0.0.0/16"
}
`)
	r := statediff.Analyze(old, new, nil)
	if len(r.SensitiveChanges) != 1 {
		t.Fatalf("SensitiveChanges = %v, want 1", r.SensitiveChanges)
	}
	if a := r.SensitiveChanges[0].AffectedResources; len(a) != 1 || a[0].MetaArg != "for_each" {
		t.Errorf("affected = %+v, want for_each", a)
	}
}

// TestAnalyzeVariableDefaultReachesCount: same logic for variable
// defaults flowing into count.
func TestAnalyzeVariableDefaultReachesCount(t *testing.T) {
	old := loadProj(t, `
variable "n" { default = 3 }
resource "aws_instance" "web" {
  count = var.n
  ami   = "ami-x"
}
`)
	new := loadProj(t, `
variable "n" { default = 1 }
resource "aws_instance" "web" {
  count = var.n
  ami   = "ami-x"
}
`)
	r := statediff.Analyze(old, new, nil)
	if len(r.SensitiveChanges) != 1 {
		t.Fatalf("SensitiveChanges = %v, want 1", r.SensitiveChanges)
	}
	c := r.SensitiveChanges[0]
	if c.Kind != "variable" || c.Name != "n" {
		t.Errorf("change = %+v, want variable.n", c)
	}
}

// loadProj writes src to a temp dir and loads it as a Project. Used
// for all the Analyze test cases.
func loadProj(t *testing.T, src string) *loader.Project {
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
