package analysis_test

import (
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
)

func TestTrackedTrailingMarker(t *testing.T) {
	mod := analyseFixture(t, `
resource "aws_eks_cluster" "this" {
  cluster_version = "1.28" # tflens:track: bump only after add-on check
}
`)
	tr := mod.TrackedAttributes()
	if len(tr) != 1 {
		t.Fatalf("want 1 tracked attribute, got %d: %+v", len(tr), tr)
	}
	if tr[0].EntityID != "resource.aws_eks_cluster.this" || tr[0].AttrName != "cluster_version" {
		t.Errorf("unexpected key: %+v", tr[0])
	}
	if tr[0].ExprText != `"1.28"` {
		t.Errorf("ExprText = %q, want %q", tr[0].ExprText, `"1.28"`)
	}
	if !strings.Contains(tr[0].Description, "add-on check") {
		t.Errorf("description = %q, want add-on check substring", tr[0].Description)
	}
}

func TestTrackedOwnLineMarkerAppliesToNextAttribute(t *testing.T) {
	mod := analyseFixture(t, `
resource "aws_eks_cluster" "this" {
  name = "prod"
  # tflens:track
  cluster_version = "1.28"
}
`)
	tr := mod.TrackedAttributes()
	if len(tr) != 1 || tr[0].AttrName != "cluster_version" {
		t.Fatalf("want single cluster_version tracked, got: %+v", tr)
	}
}

func TestTrackedDoubleSlashCommentRecognised(t *testing.T) {
	mod := analyseFixture(t, `
resource "aws_eks_cluster" "this" {
  cluster_version = "1.28" // tflens:track: slash form
}
`)
	tr := mod.TrackedAttributes()
	if len(tr) != 1 {
		t.Fatalf("want 1 tracked attribute, got %d", len(tr))
	}
	if !strings.Contains(tr[0].Description, "slash form") {
		t.Errorf("description = %q", tr[0].Description)
	}
}

func TestTrackedMarkerWithoutDescription(t *testing.T) {
	mod := analyseFixture(t, `
resource "aws_eks_cluster" "this" {
  cluster_version = "1.28" # tflens:track
}
`)
	tr := mod.TrackedAttributes()
	if len(tr) != 1 {
		t.Fatalf("want 1 tracked attribute, got %d", len(tr))
	}
	if tr[0].Description != "" {
		t.Errorf("description should be empty, got %q", tr[0].Description)
	}
}

func TestTrackedRefsResolveVariableDefault(t *testing.T) {
	mod := analyseFixture(t, `
variable "cluster_version" {
  type    = string
  default = "1.28"
}

resource "aws_eks_cluster" "this" {
  cluster_version = var.cluster_version # tflens:track
}
`)
	tr := mod.TrackedAttributes()
	if len(tr) != 1 {
		t.Fatalf("want 1 tracked attribute, got %d", len(tr))
	}
	got := tr[0].Refs["variable.cluster_version"]
	if got != `"1.28"` {
		t.Errorf("Refs[variable.cluster_version] = %q, want %q", got, `"1.28"`)
	}
}

func TestTrackedRefsRecurseThroughLocals(t *testing.T) {
	mod := analyseFixture(t, `
locals {
  inner = "1.28"
  outer = local.inner
}

resource "aws_eks_cluster" "this" {
  cluster_version = local.outer # tflens:track
}
`)
	tr := mod.TrackedAttributes()
	if len(tr) != 1 {
		t.Fatalf("want 1 tracked attribute, got %d", len(tr))
	}
	if _, ok := tr[0].Refs["local.outer"]; !ok {
		t.Errorf("Refs missing local.outer: %v", tr[0].Refs)
	}
	if _, ok := tr[0].Refs["local.inner"]; !ok {
		t.Errorf("Refs missing local.inner (should recurse): %v", tr[0].Refs)
	}
}

// TestTrackedRefsCycleProtection ensures gatherRefs terminates when two
// locals reference each other (Terraform itself rejects this at plan
// time, but the analyser must not loop indefinitely on broken input).
func TestTrackedRefsCycleProtection(t *testing.T) {
	mod := analyseFixture(t, `
locals {
  a = local.b
  b = local.a
}

resource "aws_eks_cluster" "this" {
  cluster_version = local.a # tflens:track
}
`)
	tr := mod.TrackedAttributes()
	if len(tr) != 1 {
		t.Fatalf("want 1 tracked attribute, got %d", len(tr))
	}
	// Both locals should be recorded exactly once; no infinite loop.
	if _, ok := tr[0].Refs["local.a"]; !ok {
		t.Errorf("Refs missing local.a: %v", tr[0].Refs)
	}
	if _, ok := tr[0].Refs["local.b"]; !ok {
		t.Errorf("Refs missing local.b: %v", tr[0].Refs)
	}
}

func TestTrackedNonMarkerCommentIgnored(t *testing.T) {
	mod := analyseFixture(t, `
resource "aws_eks_cluster" "this" {
  cluster_version = "1.28" # not a tracking marker
  name            = "prod" # tflens:tracking — superficially similar but wrong
}
`)
	if got := mod.TrackedAttributes(); len(got) != 0 {
		t.Errorf("non-marker comments should not produce tracked attrs, got: %+v", got)
	}
}

// TestTrackedLocalsBlockTrailingMarker confirms a marker on a local
// declaration binds to that local as its own entity (local.<name>),
// not to whatever entity contains the locals block (there isn't one —
// locals lives at the top level).
func TestTrackedLocalsBlockTrailingMarker(t *testing.T) {
	mod := analyseFixture(t, `
locals {
  cluster_version = "1.34" # tflens:track: source of truth for EKS minor
  unrelated       = "x"
}
`)
	tr := mod.TrackedAttributes()
	if len(tr) != 1 {
		t.Fatalf("want 1 tracked attribute, got %d: %+v", len(tr), tr)
	}
	if tr[0].EntityID != "local.cluster_version" || tr[0].AttrName != "value" {
		t.Errorf("unexpected key parts: entity=%q attr=%q", tr[0].EntityID, tr[0].AttrName)
	}
	if tr[0].ExprText != `"1.34"` {
		t.Errorf("ExprText = %q, want %q", tr[0].ExprText, `"1.34"`)
	}
	if !strings.Contains(tr[0].Description, "source of truth") {
		t.Errorf("description = %q", tr[0].Description)
	}
}

func TestTrackedLocalsBlockOwnLineMarker(t *testing.T) {
	mod := analyseFixture(t, `
locals {
  unrelated       = "x"
  # tflens:track: own-line above the local
  cluster_version = "1.34"
}
`)
	tr := mod.TrackedAttributes()
	if len(tr) != 1 {
		t.Fatalf("want 1 tracked attribute, got %d: %+v", len(tr), tr)
	}
	if tr[0].EntityID != "local.cluster_version" {
		t.Errorf("EntityID = %q, want local.cluster_version", tr[0].EntityID)
	}
}

// TestTrackedLocalsBlockResolvesIndirectVarRefs confirms that markers
// on locals still get the indirection walker's transitive var/local
// resolution — this is the use case that justifies marking the local
// in the first place: it's the source of truth that other things
// derive from.
func TestTrackedLocalsBlockResolvesIndirectVarRefs(t *testing.T) {
	mod := analyseFixture(t, `
variable "upgrade" {
  type    = bool
  default = true
}

locals {
  cluster_version = var.upgrade ? "1.35" : "1.34" # tflens:track
}
`)
	tr := mod.TrackedAttributes()
	if len(tr) != 1 {
		t.Fatalf("want 1 tracked attribute, got %d", len(tr))
	}
	if got := tr[0].Refs["variable.upgrade"]; got != "true" {
		t.Errorf("Refs[variable.upgrade] = %q, want %q", got, "true")
	}
}

func TestTrackedKeyStable(t *testing.T) {
	mod := analyseFixture(t, `
resource "aws_eks_cluster" "this" {
  cluster_version = "1.28" # tflens:track
}
`)
	tr := mod.TrackedAttributes()
	if len(tr) != 1 {
		t.Fatalf("want 1 tracked attribute")
	}
	got := tr[0].Key()
	want := "resource.aws_eks_cluster.this.cluster_version"
	if got != want {
		t.Errorf("Key() = %q, want %q", got, want)
	}
}

// TestTrackedRefsSortedRefIDsDeterministic ensures the helper returns a
// stable iteration order, which the diff pass relies on for
// reproducible output.
func TestTrackedRefsSortedRefIDsDeterministic(t *testing.T) {
	tr := analysis.TrackedAttribute{
		Refs: map[string]string{
			"variable.b": "1",
			"local.a":    "2",
			"variable.a": "3",
		},
	}
	got := tr.SortedRefIDs()
	want := []string{"local.a", "variable.a", "variable.b"}
	if len(got) != len(want) {
		t.Fatalf("SortedRefIDs length = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("SortedRefIDs[%d] = %q, want %q", i, got[i], want[i])
		}
	}
}
