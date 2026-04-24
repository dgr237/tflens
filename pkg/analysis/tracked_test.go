package analysis_test

import (
	"strings"
	"testing"

	"github.com/hashicorp/hcl/v2"

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

// TestTrackedHelperCases is the table-driven entry point for the
// LookupAttrText / EvalContext / GatherRefsFromExpr helpers that
// tracked-attribute resolution and pkg/diff's tracked diff layer
// use. Each case loads testdata/tracked/<Name>/main.tf and runs a
// per-case Custom assertion. Cases that don't need a fixture (nil-
// receiver paths) leave NoFixture=true.
func TestTrackedHelperCases(t *testing.T) {
	for _, tc := range trackedHelperCases {
		t.Run(tc.Name, func(t *testing.T) {
			var m *analysis.Module
			if !tc.NoFixture {
				src := loadAnalysisFixture(t, "tracked", tc.Name)
				m = analyseFixtureNamed(t, "main.tf", src)
			}
			tc.Custom(t, m)
		})
	}
}

type trackedHelperCase struct {
	Name      string
	NoFixture bool // skip fixture load (nil-receiver tests)
	Custom    func(t *testing.T, m *analysis.Module)
}

var trackedHelperCases = []trackedHelperCase{
	{
		Name: "lookup_attr_locals_outputs",
		Custom: func(t *testing.T, m *analysis.Module) {
			expectLookup(t, m, "local.cluster_version", "value", `"1.34"`, true)
			expectLookup(t, m, "local.cluster_version", "cluster_version", `"1.34"`, true)
			expectLookup(t, m, "output.name", "value", `"prod"`, true)
		},
	},
	{
		Name: "lookup_attr_variable_default",
		Custom: func(t *testing.T, m *analysis.Module) {
			expectLookup(t, m, "variable.with_def", "default", `"x"`, true)
			// Declared but no default → ("", true), distinct from unknown.
			expectLookup(t, m, "variable.without_def", "default", "", true)
		},
	},
	{
		Name: "lookup_attr_module_arg",
		Custom: func(t *testing.T, m *analysis.Module) {
			expectLookup(t, m, "module.kid", "passed", `"hello"`, true)
			// An unpassed arg legitimately doesn't exist on the entity.
			expectLookup(t, m, "module.kid", "not_passed", "", false)
		},
	},
	{
		Name: "lookup_attr_resource_metas",
		Custom: func(t *testing.T, m *analysis.Module) {
			cases := []struct{ id, attr string }{
				{"resource.aws_instance.all_metas", "count"},
				{"resource.aws_instance.all_metas", "depends_on"},
				{"resource.aws_instance.all_metas", "provider"},
				{"resource.aws_instance.all_metas", "ignore_changes"},
				{"resource.aws_instance.all_metas", "replace_triggered_by"},
				{"resource.aws_instance.by_each", "for_each"},
			}
			for _, c := range cases {
				got, ok := m.LookupAttrText(c.id, c.attr)
				if !ok || got == "" {
					t.Errorf("LookupAttrText(%q, %q) = (%q, %v), want non-empty + true",
						c.id, c.attr, got, ok)
				}
			}
		},
	},
	{
		Name: "lookup_attr_unknown",
		Custom: func(t *testing.T, m *analysis.Module) {
			expectLookup(t, m, "variable.nonexistent", "default", "", false)
			expectLookup(t, m, "variable.v", "no_such_attr", "", false)
		},
	},
	{
		Name: "eval_context_var_local",
		Custom: func(t *testing.T, m *analysis.Module) {
			ctx := m.EvalContext()
			if got := evalToString(t, ctx, "local.label"); got != "size-6" {
				t.Errorf("local.label = %q, want %q", got, "size-6")
			}
			if got := evalToString(t, ctx, "var.size"); got != "3" {
				t.Errorf("var.size = %q, want %q", got, "3")
			}
		},
	},
	{
		Name: "eval_context_unevaluable",
		Custom: func(t *testing.T, m *analysis.Module) {
			ctx := m.EvalContext()
			if ctx == nil {
				t.Fatal("EvalContext returned nil")
			}
			// var.ok must be present and evaluable.
			if got := evalToString(t, ctx, "var.ok"); got != "yes" {
				t.Errorf("var.ok = %q, want %q", got, "yes")
			}
		},
	},
	{
		Name:      "eval_context_nil_receiver",
		NoFixture: true,
		Custom: func(t *testing.T, _ *analysis.Module) {
			var nilMod *analysis.Module
			ctx := nilMod.EvalContext()
			if ctx == nil {
				t.Fatal("expected non-nil EvalContext for nil receiver")
			}
			if len(ctx.Variables) != 0 {
				t.Errorf("expected empty Variables, got %v", ctx.Variables)
			}
		},
	},
	{
		Name: "gather_refs_var_local",
		Custom: func(t *testing.T, m *analysis.Module) {
			var expr *analysis.Expr
			for _, e := range m.Filter(analysis.KindOutput) {
				if e.Name == "summary" {
					expr = e.ValueExpr
				}
			}
			if expr == nil {
				t.Fatal("missing output.summary expr")
			}
			refs := m.GatherRefsFromExpr(expr)
			if got := refs["variable.name"]; got != `"prod"` {
				t.Errorf(`refs["variable.name"] = %q, want "\"prod\""`, got)
			}
			if got := refs["local.region"]; got != `"us-east-1"` {
				t.Errorf(`refs["local.region"] = %q, want "\"us-east-1\""`, got)
			}
		},
	},
	{
		Name:      "gather_refs_nil_inputs",
		NoFixture: true,
		Custom: func(t *testing.T, _ *analysis.Module) {
			var nilMod *analysis.Module
			if got := nilMod.GatherRefsFromExpr(nil); got == nil || len(got) != 0 {
				t.Errorf("nil receiver + nil expr = %v", got)
			}
		},
	},
	{
		Name: "gather_refs_real_module_nil_expr",
		Custom: func(t *testing.T, m *analysis.Module) {
			if got := m.GatherRefsFromExpr(nil); got == nil || len(got) != 0 {
				t.Errorf("real receiver + nil expr = %v", got)
			}
			if got := m.GatherRefsFromExpr(&analysis.Expr{}); got == nil || len(got) != 0 {
				t.Errorf("real receiver + zero Expr = %v", got)
			}
		},
	},
}

// expectLookup runs LookupAttrText and asserts both return values.
func expectLookup(t *testing.T, m *analysis.Module, id, attr, wantText string, wantOK bool) {
	t.Helper()
	got, ok := m.LookupAttrText(id, attr)
	if got != wantText || ok != wantOK {
		t.Errorf("LookupAttrText(%q, %q) = (%q, %v), want (%q, %v)",
			id, attr, got, ok, wantText, wantOK)
	}
}

// evalToString evaluates `expr` against ctx and returns its text form.
// Numbers come back as their decimal string; strings as the raw string.
// Used by the EvalContext cases to assert effective values without
// hand-rolling a hcl evaluation harness in each case.
func evalToString(t *testing.T, ctx *hcl.EvalContext, expr string) string {
	t.Helper()
	parsed := parseToFile(t, "expr.tf", `output "x" { value = `+expr+` }`)
	if len(parsed.Body.Blocks) == 0 {
		t.Fatalf("no blocks parsed from expr %q", expr)
	}
	b := parsed.Body.Blocks[0]
	v, diags := b.Body.Attributes["value"].Expr.Value(ctx)
	if diags.HasErrors() {
		t.Fatalf("evaluating %s: %v", expr, diags)
	}
	if v.Type().FriendlyName() == "number" {
		return v.AsBigFloat().Text('f', -1)
	}
	return v.AsString()
}
