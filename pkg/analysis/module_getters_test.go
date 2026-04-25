package analysis_test

import (
	"reflect"
	"sort"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
)

// TestModuleGettersCases is the table entry point for the small
// terraform { … } / moved / removed / module-call getters that
// pkg/diff and pkg/loader read from. Each case loads
// testdata/module_getters/<Name>/main.tf and asserts via Custom.
func TestModuleGettersCases(t *testing.T) {
	for _, tc := range moduleGettersCases {
		t.Run(tc.Name, func(t *testing.T) {
			src := loadAnalysisFixture(t, "module_getters", tc.Name)
			m := analyseFixtureNamed(t, "main.tf", src)
			tc.Custom(t, m)
		})
	}
}

type moduleGettersCase struct {
	Name   string
	Custom func(t *testing.T, m *analysis.Module)
}

var moduleGettersCases = []moduleGettersCase{
	{
		Name: "full",
		Custom: func(t *testing.T, m *analysis.Module) {
			// terraform { required_version = ... }
			if got := m.RequiredVersion(); got != ">= 1.5.0" {
				t.Errorf("RequiredVersion = %q, want %q", got, ">= 1.5.0")
			}
			// terraform { required_providers { ... } }
			rp := m.RequiredProviders()
			if got := rp["aws"]; got.Source != "hashicorp/aws" || got.Version != "~> 5.0" {
				t.Errorf("required_providers.aws = %+v", got)
			}
			if got := rp["random"]; got.Source != "hashicorp/random" || got.Version != "" {
				t.Errorf("required_providers.random = %+v (version should be empty)", got)
			}
			// Returned map is a copy — mutating it does not affect future calls.
			rp["aws"] = analysis.ProviderRequirement{Source: "evil"}
			if again := m.RequiredProviders(); again["aws"].Source != "hashicorp/aws" {
				t.Errorf("RequiredProviders should return a copy, mutation leaked: %+v", again["aws"])
			}
			// terraform { backend "s3" { ... } }
			b := m.Backend()
			if b == nil || b.Type != "s3" {
				t.Fatalf("Backend = %+v, want non-nil s3", b)
			}
			if got := b.Config["bucket"]; got != `"tfstate"` {
				t.Errorf(`backend.bucket = %q, want "\"tfstate\""`, got)
			}
			if got := b.Config["region"]; got != `"us-east-1"` {
				t.Errorf("backend.region = %q", got)
			}
			// module "vpc" { source/version }
			if got := m.ModuleSource("vpc"); got != "registry.example.com/ns/vpc/aws" {
				t.Errorf("ModuleSource(vpc) = %q", got)
			}
			if got := m.ModuleVersion("vpc"); got != "1.2.3" {
				t.Errorf("ModuleVersion(vpc) = %q", got)
			}
			if got := m.ModuleSource("local_kid"); got != "./child" {
				t.Errorf("ModuleSource(local_kid) = %q", got)
			}
			if got := m.ModuleVersion("local_kid"); got != "" {
				t.Errorf("ModuleVersion(local_kid) = %q, want empty (no version)", got)
			}
			// ModuleOutputReferences: aws_instance.web reads
			// module.vpc.public_subnet_id and depends on module.vpc
			// (bare ref, no attribute) — only the attribute reference
			// should appear, not the bare one.
			refs := m.ModuleOutputReferences("vpc")
			want := []string{"public_subnet_id"}
			if !reflect.DeepEqual(refs, want) {
				t.Errorf("ModuleOutputReferences(vpc) = %v, want %v", refs, want)
			}
			// moved: aws_instance.old_web → aws_instance.web
			moved := m.Moved()
			if got := moved["resource.aws_instance.old_web"]; got != "resource.aws_instance.web" {
				t.Errorf("Moved[aws_instance.old_web] = %q, want resource.aws_instance.web", got)
			}
			// removed: aws_instance.legacy
			if !m.RemovedDeclared("resource.aws_instance.legacy") {
				t.Error("RemovedDeclared(aws_instance.legacy) = false, want true")
			}
			if m.RemovedDeclared("resource.aws_instance.web") {
				t.Error("RemovedDeclared(aws_instance.web) = true, want false (not in a removed block)")
			}
		},
	},
	{
		Name: "moved_data",
		Custom: func(t *testing.T, m *analysis.Module) {
			// moved on data sources and on module calls — exercises
			// refPartsToEntityID's data and module branches.
			moved := m.Moved()
			if got := moved["data.aws_ami.old"]; got != "data.aws_ami.new" {
				t.Errorf("Moved[data.aws_ami.old] = %q", got)
			}
			if got := moved["module.old_call"]; got != "module.new_call" {
				t.Errorf("Moved[module.old_call] = %q", got)
			}
			// Sorted view for stable output (also exercises iteration).
			keys := make([]string, 0, len(moved))
			for k := range moved {
				keys = append(keys, k)
			}
			sort.Strings(keys)
			want := []string{"data.aws_ami.old", "module.old_call"}
			if !reflect.DeepEqual(keys, want) {
				t.Errorf("moved keys = %v, want %v", keys, want)
			}
		},
	},
	{
		Name: "empty",
		Custom: func(t *testing.T, m *analysis.Module) {
			// No declarations — every getter returns its zero form
			// without panicking.
			if got := m.RequiredVersion(); got != "" {
				t.Errorf("RequiredVersion = %q, want empty", got)
			}
			if got := m.RequiredProviders(); len(got) != 0 {
				t.Errorf("RequiredProviders = %v, want empty", got)
			}
			if b := m.Backend(); b != nil {
				t.Errorf("Backend = %+v, want nil", b)
			}
			if got := m.Moved(); len(got) != 0 {
				t.Errorf("Moved = %v, want empty", got)
			}
			if m.RemovedDeclared("anything") {
				t.Error("RemovedDeclared(anything) = true on empty module")
			}
			if got := m.ModuleSource("none"); got != "" {
				t.Errorf("ModuleSource(none) = %q", got)
			}
			if got := m.ModuleVersion("none"); got != "" {
				t.Errorf("ModuleVersion(none) = %q", got)
			}
			if got := m.ModuleOutputReferences("none"); len(got) != 0 {
				t.Errorf("ModuleOutputReferences(none) = %v, want empty", got)
			}
		},
	},
	{
		Name: "expr_methods",
		Custom: func(t *testing.T, m *analysis.Module) {
			// Variable with a default — its DefaultExpr is a real Expr;
			// Range/Pos must produce a non-zero source position.
			var sizeVar *analysis.Entity
			for _, e := range m.Filter(analysis.KindVariable) {
				if e.Name == "size" {
					sizeVar = &e
				}
			}
			if sizeVar == nil || sizeVar.DefaultExpr == nil {
				t.Fatal("missing variable.size with DefaultExpr")
			}
			r := sizeVar.DefaultExpr.Range()
			if r.Filename == "" || r.Start.Line == 0 {
				t.Errorf("DefaultExpr.Range() = %+v, want populated filename + line", r)
			}
			pos := sizeVar.DefaultExpr.Pos()
			if pos.File == "" || pos.Line == 0 {
				t.Errorf("DefaultExpr.Pos() = %+v, want populated File + Line", pos)
			}
			// Nil-safe: the methods should return zero values, not panic.
			var nilExpr *analysis.Expr
			if r := nilExpr.Range(); r.Filename != "" {
				t.Errorf("nil Expr.Range() = %+v, want zero", r)
			}
			if pos := nilExpr.Pos(); pos.File != "" || pos.Line != 0 {
				t.Errorf("nil Expr.Pos() = %+v, want zero", pos)
			}
			// validation/precondition/postcondition conditions: each should
			// have one entry whose canonical condition text matches the
			// declared expression. Post-migration the field is a structured
			// []ConditionBlock — assertions go through ConditionText().
			if got := sizeVar.Validations; len(got) != 1 || got[0].ConditionText() != "var.size > 0" {
				t.Errorf("Validations = %v, want one block with text [var.size > 0]", got)
			}
			var web *analysis.Entity
			for _, e := range m.Filter(analysis.KindResource) {
				if e.Name == "web" {
					web = &e
				}
			}
			if web == nil {
				t.Fatal("missing resource.aws_instance.web")
			}
			if got := web.Preconditions; len(got) != 1 || got[0].ConditionText() != "var.size <= 100" {
				t.Errorf("Preconditions = %v, want one block with text [var.size <= 100]", got)
			}
			if got := web.Postconditions; len(got) != 1 || got[0].ConditionText() != `self.id != ""` {
				t.Errorf(`Postconditions = %v, want one block with text [self.id != ""]`, got)
			}
		},
	},
	{
		Name: "nil_receiver",
		Custom: func(t *testing.T, _ *analysis.Module) {
			// The fixture is loaded but ignored — what we're testing is
			// that the nil-safe paths added for pkg/diff still hold.
			var nilMod *analysis.Module
			if got := nilMod.RequiredVersion(); got != "" {
				t.Errorf("nil.RequiredVersion = %q", got)
			}
			if got := nilMod.RequiredProviders(); len(got) != 0 {
				t.Errorf("nil.RequiredProviders = %v", got)
			}
			if b := nilMod.Backend(); b != nil {
				t.Errorf("nil.Backend = %+v", b)
			}
			if got := nilMod.Moved(); len(got) != 0 {
				t.Errorf("nil.Moved = %v", got)
			}
			if nilMod.RemovedDeclared("x") {
				t.Error("nil.RemovedDeclared = true")
			}
			if got := nilMod.ModuleSource("x"); got != "" {
				t.Errorf("nil.ModuleSource = %q", got)
			}
			if got := nilMod.ModuleVersion("x"); got != "" {
				t.Errorf("nil.ModuleVersion = %q", got)
			}
			if got := nilMod.ModuleOutputReferences("x"); got != nil {
				t.Errorf("nil.ModuleOutputReferences = %v, want nil", got)
			}
			if got := nilMod.Validate(); got != nil {
				t.Errorf("nil.Validate = %v, want nil", got)
			}
			if got := nilMod.Entities(); got != nil {
				t.Errorf("nil.Entities = %v, want nil", got)
			}
			if got := nilMod.Filter(analysis.KindResource); got != nil {
				t.Errorf("nil.Filter = %v, want nil", got)
			}
			if nilMod.HasEntity("x") {
				t.Error("nil.HasEntity = true")
			}
			if _, ok := nilMod.EntityByID("x"); ok {
				t.Error("nil.EntityByID returned ok=true")
			}
		},
	},
}
