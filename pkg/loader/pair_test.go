package loader_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/dgr237/tflens/pkg/loader"
)

func TestLeafSegment(t *testing.T) {
	cases := map[string]string{
		"vpc":         "vpc",
		"vpc.sg":      "sg",
		"a.b.c":       "c",
		"":            "",
		".":           "",
		"trailing.":   "",
		".leading":    "leading",
	}
	for in, want := range cases {
		if got := loader.LeafSegment(in); got != want {
			t.Errorf("LeafSegment(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestModuleCallStatusString(t *testing.T) {
	cases := map[loader.ModuleCallStatus]string{
		loader.StatusChanged: "changed",
		loader.StatusAdded:   "added",
		loader.StatusRemoved: "removed",
		loader.ModuleCallStatus(99): "changed", // unknown falls back to "changed"
	}
	for s, want := range cases {
		if got := s.String(); got != want {
			t.Errorf("Status(%d).String() = %q, want %q", s, got, want)
		}
	}
}

// TestPairModuleCallsBothNil confirms the all-nil safety case — no
// panic, empty result.
func TestPairModuleCallsBothNil(t *testing.T) {
	got := loader.PairModuleCalls(nil, nil)
	if len(got) != 0 {
		t.Errorf("nil/nil: got %d pairs, want 0", len(got))
	}
}

// TestPairModuleCallsAddedOnly: a project that only exists in NEW.
// All pairs should be StatusAdded with only New* fields populated.
func TestPairModuleCallsAddedOnly(t *testing.T) {
	root := writeMiniProject(t, `
module "vpc" {
  source  = "ns/vpc/aws"
  version = "1.0.0"
}
`)
	newProj := loadProjectOrFail(t, root)
	pairs := loader.PairModuleCalls(nil, newProj)
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
}

// TestPairModuleCallsRemovedOnly: a project that only exists in OLD.
// All pairs should be StatusRemoved.
func TestPairModuleCallsRemovedOnly(t *testing.T) {
	root := writeMiniProject(t, `
module "vpc" {
  source = "ns/vpc/aws"
}
`)
	oldProj := loadProjectOrFail(t, root)
	pairs := loader.PairModuleCalls(oldProj, nil)
	if len(pairs) != 1 {
		t.Fatalf("got %d pairs, want 1", len(pairs))
	}
	if pairs[0].Status != loader.StatusRemoved {
		t.Errorf("Status = %v, want StatusRemoved", pairs[0].Status)
	}
	if pairs[0].OldSource != "ns/vpc/aws" {
		t.Errorf("OldSource = %q, want ns/vpc/aws", pairs[0].OldSource)
	}
}

// TestPairModuleCallsChangedAcrossSides: same key in both, with
// different sources/versions. Both Old* and New* fields populated.
func TestPairModuleCallsChangedAcrossSides(t *testing.T) {
	oldRoot := writeMiniProject(t, `
module "vpc" {
  source  = "ns/vpc/aws"
  version = "1.0.0"
}
`)
	newRoot := writeMiniProject(t, `
module "vpc" {
  source  = "ns/vpc/aws"
  version = "2.0.0"
}
`)
	pairs := loader.PairModuleCalls(loadProjectOrFail(t, oldRoot), loadProjectOrFail(t, newRoot))
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
}

// TestPairModuleCallsCoversAddedRemovedTogether mixes adds + removes
// in the same diff to confirm the union over keys works correctly.
func TestPairModuleCallsCoversAddedRemovedTogether(t *testing.T) {
	oldRoot := writeMiniProject(t, `
module "removed_one" { source = "ns/removed/aws" }
module "kept"        { source = "ns/kept/aws" }
`)
	newRoot := writeMiniProject(t, `
module "kept"      { source = "ns/kept/aws" }
module "added_one" { source = "ns/added/aws" }
`)
	pairs := loader.PairModuleCalls(loadProjectOrFail(t, oldRoot), loadProjectOrFail(t, newRoot))
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
}

// TestPairModuleCallsRecursesIntoChildren: nested modules produce
// dotted keys and are paired independently.
func TestPairModuleCallsRecursesIntoChildren(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "main.tf"), `
module "vpc" {
  source = "./modules/vpc"
}
`)
	if err := os.MkdirAll(filepath.Join(root, "modules", "vpc"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "modules", "vpc", "main.tf"), `
module "sg" {
  source = "./sg"
}
`)
	if err := os.MkdirAll(filepath.Join(root, "modules", "vpc", "sg"), 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(root, "modules", "vpc", "sg", "main.tf"), `
resource "aws_security_group" "this" {}
`)

	proj := loadProjectOrFail(t, root)
	pairs := loader.PairModuleCalls(nil, proj)
	keys := make([]string, len(pairs))
	for i, p := range pairs {
		keys[i] = p.Key
	}
	sort.Strings(keys)
	want := []string{"vpc", "vpc.sg"}
	if len(keys) != len(want) || keys[0] != want[0] || keys[1] != want[1] {
		t.Errorf("keys = %v, want %v", keys, want)
	}
}

// ---- helpers ----

// writeMiniProject creates a temp dir with a single main.tf and
// returns the root path.
func writeMiniProject(t *testing.T, src string) string {
	t.Helper()
	dir := t.TempDir()
	mustWrite(t, filepath.Join(dir, "main.tf"), src)
	return dir
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func loadProjectOrFail(t *testing.T, root string) *loader.Project {
	t.Helper()
	proj, _, err := loader.LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject(%s): %v", root, err)
	}
	return proj
}
