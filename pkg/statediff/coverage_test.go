package statediff_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/statediff"
)

// loadProjPair writes oldSrc + newSrc to two temp dirs and returns
// the loaded projects. Lets each coverageCase express its old/new
// pair inline, the same shape statediff.Analyze takes in production.
func loadProjPair(t *testing.T, oldSrc, newSrc string) (oldProj, newProj *loader.Project) {
	t.Helper()
	return loadProj(t, oldSrc), loadProj(t, newSrc)
}

// loadNestedProj writes a parent main.tf plus one or more children
// described by name → src so the walkAllModules + nested-detect
// paths are reachable from a test. Each child is created at
// <tmp>/<name>/main.tf and referenced from the parent via
// `source = "./<name>"`.
func loadNestedProj(t *testing.T, parentSrc string, children map[string]string) *loader.Project {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.tf"), []byte(parentSrc), 0o644); err != nil {
		t.Fatal(err)
	}
	for name, src := range children {
		childDir := filepath.Join(dir, name)
		if err := os.MkdirAll(childDir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(childDir, "main.tf"), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	proj, _, err := loader.LoadProject(dir)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	return proj
}

// coverageCase describes one Analyze-result assertion targeting a
// specific branch of statediff that the existing tests don't reach.
// Each case builds its old + new projects via Setup, runs Analyze,
// and runs Custom on the result.
type coverageCase struct {
	Name   string
	Setup  func(t *testing.T) (oldProj, newProj *loader.Project)
	Custom func(t *testing.T, r statediff.Result)
}

func TestStatediffCoverageCases(t *testing.T) {
	for _, tc := range coverageCases {
		t.Run(tc.Name, func(t *testing.T) {
			old, newp := tc.Setup(t)
			tc.Custom(t, statediff.Analyze(old, newp, nil))
		})
	}
}

var coverageCases = []coverageCase{
	{
		// Two resources sharing the same sensitive local: mergeSensitive
		// coalesces them under one SensitiveChange whose AffectedResources
		// list has both. Exercises the "merge into existing entry" path.
		Name: "merge_sensitive_two_resources_same_local",
		Setup: func(t *testing.T) (*loader.Project, *loader.Project) {
			return loadProjPair(t,
				`locals { n = 2 }
resource "aws_instance" "a" { count = local.n  ami = "x" }
resource "aws_instance" "b" { count = local.n  ami = "y" }`,
				`locals { n = 1 }
resource "aws_instance" "a" { count = local.n  ami = "x" }
resource "aws_instance" "b" { count = local.n  ami = "y" }`,
			)
		},
		Custom: func(t *testing.T, r statediff.Result) {
			if len(r.SensitiveChanges) != 1 {
				t.Fatalf("SensitiveChanges = %v, want 1 (merged)", r.SensitiveChanges)
			}
			affected := r.SensitiveChanges[0].AffectedResources
			if len(affected) != 2 {
				t.Errorf("AffectedResources len = %d, want 2 (merged)", len(affected))
			}
		},
	},
	{
		// Variable with no default: variableDefaultsMap should map it
		// to the empty string (not crash on nil DefaultExpr). Pair
		// against a version whose default is set so the change shows
		// up — confirms the nil-DefaultExpr branch executes.
		Name: "variable_without_default_maps_to_empty",
		Setup: func(t *testing.T) (*loader.Project, *loader.Project) {
			return loadProjPair(t,
				`variable "n" {}
resource "aws_instance" "x" { count = var.n  ami = "y" }`,
				`variable "n" { default = 3 }
resource "aws_instance" "x" { count = var.n  ami = "y" }`,
			)
		},
		Custom: func(t *testing.T, r statediff.Result) {
			if len(r.SensitiveChanges) != 1 {
				t.Fatalf("SensitiveChanges = %v, want 1", r.SensitiveChanges)
			}
			c := r.SensitiveChanges[0]
			if c.OldValue != "" || c.NewValue != "3" {
				t.Errorf("got (old=%q, new=%q), want (\"\", \"3\")", c.OldValue, c.NewValue)
			}
		},
	},
	{
		// Local that flows through another local before reaching count:
		// outer = local.inner, count = length(local.outer). Changing
		// inner triggers the change via the transitive-deps loop in
		// refsReachingTargets.
		Name: "local_transitive_through_intermediate",
		Setup: func(t *testing.T) (*loader.Project, *loader.Project) {
			return loadProjPair(t,
				`locals {
  inner = "abc"
  outer = local.inner
}
resource "aws_instance" "x" { count = length(local.outer)  ami = "y" }`,
				`locals {
  inner = "ab"
  outer = local.inner
}
resource "aws_instance" "x" { count = length(local.outer)  ami = "y" }`,
			)
		},
		Custom: func(t *testing.T, r statediff.Result) {
			found := false
			for _, c := range r.SensitiveChanges {
				if c.Name == "inner" {
					found = true
				}
			}
			if !found {
				t.Errorf("expected local.inner to be flagged via transitive ref; got %+v", r.SensitiveChanges)
			}
		},
	},
	{
		// count expression that references both a local AND a non-
		// target entity (module / resource / data). Forces
		// refToEntityID through its module / resource fallback
		// branches even though no flag fires for those refs.
		Name: "count_refs_module_resource_data_alongside_local",
		Setup: func(t *testing.T) (*loader.Project, *loader.Project) {
			return loadProjPair(t,
				`locals { n = 2 }
module "m" { source = "ns/m/aws" }
data "aws_subnets" "s" {}
resource "aws_instance" "other" {}
resource "aws_instance" "x" {
  count = local.n + length(module.m.ids) + length(data.aws_subnets.s.ids) + length(aws_instance.other)
  ami   = "y"
}`,
				`locals { n = 1 }
module "m" { source = "ns/m/aws" }
data "aws_subnets" "s" {}
resource "aws_instance" "other" {}
resource "aws_instance" "x" {
  count = local.n + length(module.m.ids) + length(data.aws_subnets.s.ids) + length(aws_instance.other)
  ami   = "y"
}`,
			)
		},
		Custom: func(t *testing.T, r statediff.Result) {
			// We only assert the local got flagged — the module / data
			// / resource branches in refToEntityID just need to RUN
			// (the targets map only contains local.n; the non-local
			// refs are silently ignored after the entity-ID lookup).
			if len(r.SensitiveChanges) != 1 {
				t.Errorf("SensitiveChanges len = %d, want 1; got %+v",
					len(r.SensitiveChanges), r.SensitiveChanges)
			}
		},
	},
	{
		// Multiple added resources sort deterministically by Address.
		// Pin the order so sortResourceRefs's comparator path gets
		// exercised (single-resource adds bypass the sort).
		Name: "added_resources_sorted_by_address",
		Setup: func(t *testing.T) (*loader.Project, *loader.Project) {
			return loadProjPair(t,
				`# empty`,
				`resource "aws_vpc" "z_late"  {}
resource "aws_vpc" "a_early" {}
resource "aws_vpc" "m_mid"   {}`,
			)
		},
		Custom: func(t *testing.T, r statediff.Result) {
			if len(r.AddedResources) != 3 {
				t.Fatalf("AddedResources len = %d, want 3", len(r.AddedResources))
			}
			want := []string{"aws_vpc.a_early", "aws_vpc.m_mid", "aws_vpc.z_late"}
			for i, a := range r.AddedResources {
				if a.Address() != want[i] {
					t.Errorf("AddedResources[%d] = %q, want %q (sort order)", i, a.Address(), want[i])
				}
			}
		},
	},
	{
		// Nested module: parent + one local-source child. walkAllModules
		// recurses into the child; an added resource inside the child
		// surfaces under the child's "module.<name>" prefix.
		Name: "walks_into_child_module",
		Setup: func(t *testing.T) (*loader.Project, *loader.Project) {
			old := loadNestedProj(t,
				`module "kid" { source = "./kid" }`,
				map[string]string{"kid": `# empty child`},
			)
			newp := loadNestedProj(t,
				`module "kid" { source = "./kid" }`,
				map[string]string{"kid": `resource "aws_vpc" "main" {}`},
			)
			return old, newp
		},
		Custom: func(t *testing.T, r statediff.Result) {
			found := false
			for _, a := range r.AddedResources {
				if a.Module == "module.kid" && a.Name == "main" {
					found = true
				}
			}
			if !found {
				t.Errorf("expected module.kid.aws_vpc.main in adds; got %+v", r.AddedResources)
			}
		},
	},
}
