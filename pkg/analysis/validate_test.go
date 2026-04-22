package analysis_test

import (
	"testing"
	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/ast"
	"github.com/dgr237/tflens/pkg/parser"
)

// validateFixture parses src under the given filename and returns the
// validation errors produced by analysis.
func validateFixture(t *testing.T, filename, src string) []analysis.ValidationError {
	t.Helper()
	file, errs := parser.ParseFile([]byte(src), filename)
	for _, e := range errs {
		t.Errorf("parse error: %s", e)
	}
	if t.Failed() {
		t.FailNow()
	}
	return analysis.Analyse(file).Validate()
}

func TestValidateCleanModule(t *testing.T) {
	src := `
variable "env" {}
locals { prefix = var.env }
resource "aws_vpc" "main" { tags = { Env = local.prefix } }
output "id" { value = aws_vpc.main.id }
`
	errs := validateFixture(t, "main.tf", src)
	if len(errs) != 0 {
		t.Errorf("expected no validation errors, got: %v", errs)
	}
}

func TestValidateUndefinedVariable(t *testing.T) {
	src := `locals { x = var.missing }`
	errs := validateFixture(t, "main.tf", src)
	if len(errs) == 0 {
		t.Fatal("expected a validation error for undefined variable, got none")
	}
	found := false
	for _, e := range errs {
		if e.Ref == "variable.missing" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected error for variable.missing, got: %v", errs)
	}
}

func TestValidateUndefinedLocal(t *testing.T) {
	src := `output "x" { value = local.ghost }`
	errs := validateFixture(t, "main.tf", src)
	if len(errs) == 0 {
		t.Fatal("expected a validation error for undefined local, got none")
	}
	found := false
	for _, e := range errs {
		if e.Ref == "local.ghost" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected error for local.ghost, got: %v", errs)
	}
}

func TestValidateUndefinedModule(t *testing.T) {
	src := `output "vpc" { value = module.network.vpc_id }`
	errs := validateFixture(t, "main.tf", src)
	if len(errs) == 0 {
		t.Fatal("expected a validation error for undefined module, got none")
	}
	found := false
	for _, e := range errs {
		if e.Ref == "module.network" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected error for module.network, got: %v", errs)
	}
}

func TestValidateUndefinedDataSource(t *testing.T) {
	src := `resource "aws_instance" "web" { ami = data.aws_ami.ghost.id }`
	errs := validateFixture(t, "main.tf", src)
	if len(errs) == 0 {
		t.Fatal("expected a validation error for undefined data source, got none")
	}
	found := false
	for _, e := range errs {
		if e.Ref == "data.aws_ami.ghost" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected error for data.aws_ami.ghost, got: %v", errs)
	}
}

func TestValidateDefinedReferenceProducesNoError(t *testing.T) {
	// var.env, local.prefix, and data.aws_ami.ubuntu all exist — no errors.
	src := `
variable "env" {}
data "aws_ami" "ubuntu" { most_recent = true }
locals { prefix = var.env }
resource "aws_instance" "web" {
  ami  = data.aws_ami.ubuntu.id
  tags = { Name = local.prefix }
}
`
	errs := validateFixture(t, "main.tf", src)
	if len(errs) != 0 {
		t.Errorf("expected no errors, got: %v", errs)
	}
}

func TestValidateBuiltinsNotFlagged(t *testing.T) {
	// count.index, each.key, path.module, self.* are built-ins.
	src := `
resource "aws_subnet" "pub" {
  count      = 3
  cidr_block = cidrsubnet("10.0.0.0/16", 8, count.index)
}
`
	errs := validateFixture(t, "main.tf", src)
	if len(errs) != 0 {
		t.Errorf("builtins should not produce validation errors, got: %v", errs)
	}
}

func TestValidateErrorHasPosition(t *testing.T) {
	// The error position should point to the reference, not the entity.
	src := "locals { bad = var.missing }\n"
	errs := validateFixture(t, "vars.tf", src)
	if len(errs) == 0 {
		t.Fatal("expected a validation error, got none")
	}
	e := errs[0]
	if e.Pos.File != "vars.tf" {
		t.Errorf("Pos.File = %q, want %q", e.Pos.File, "vars.tf")
	}
	if e.Pos.Line != 1 {
		t.Errorf("Pos.Line = %d, want 1", e.Pos.Line)
	}
}

func TestValidateSortedByPosition(t *testing.T) {
	// Two errors: second line and first line. Output must be sorted by line.
	src := "locals {\n  a = var.missing_a\n  b = var.missing_b\n}\n"
	errs := validateFixture(t, "main.tf", src)
	if len(errs) < 2 {
		t.Fatalf("expected at least 2 validation errors, got %d", len(errs))
	}
	for i := 1; i < len(errs); i++ {
		prev, cur := errs[i-1], errs[i]
		if prev.Pos.Line > cur.Pos.Line {
			t.Errorf("errors not sorted by line: error %d (line %d) after error %d (line %d)",
				i, cur.Pos.Line, i-1, prev.Pos.Line)
		}
	}
}

// ---- sensitive propagation ----

func TestSensitiveVarReferencedByNonSensitiveOutputIsError(t *testing.T) {
	src := `
variable "password" {
  type      = string
  sensitive = true
}
output "pw" { value = var.password }
`
	errs := validateFixture(t, "main.tf", src)
	if len(errs) == 0 {
		t.Fatal("expected a validation error, got none")
	}
	found := false
	for _, e := range errs {
		if e.EntityID == "output.pw" && e.Ref == "variable.password" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected sensitive-propagation error, got: %v", errs)
	}
}

func TestSensitiveOutputReferencingSensitiveVarIsOK(t *testing.T) {
	src := `
variable "password" {
  type      = string
  sensitive = true
}
output "pw" {
  value     = var.password
  sensitive = true
}
`
	errs := validateFixture(t, "main.tf", src)
	for _, e := range errs {
		if e.EntityID == "output.pw" {
			t.Errorf("sensitive output referencing sensitive var should not be flagged, got: %v", e)
		}
	}
}

func TestNonSensitiveOutputReferencingNonSensitiveVarIsOK(t *testing.T) {
	src := `
variable "env" {
  type = string
}
output "env" { value = var.env }
`
	errs := validateFixture(t, "main.tf", src)
	for _, e := range errs {
		if e.Ref == "variable.env" {
			t.Errorf("non-sensitive var referenced by non-sensitive output should not be flagged: %v", e)
		}
	}
}

func TestSensitivePropagationDeduplicated(t *testing.T) {
	// Referencing the same sensitive var multiple times in one output
	// should produce only one error.
	src := `
variable "password" {
  type      = string
  sensitive = true
}
output "pw" {
  value = "${var.password} and again ${var.password}"
}
`
	errs := validateFixture(t, "main.tf", src)
	count := 0
	for _, e := range errs {
		if e.EntityID == "output.pw" && e.Ref == "variable.password" {
			count++
		}
	}
	if count != 1 {
		t.Errorf("expected exactly 1 sensitive-propagation error, got %d", count)
	}
}

func TestValidateMultipleFilesAggregated(t *testing.T) {
	// AnalyseFiles merges two files; undefined refs from both are reported.
	src1 := "locals { a = var.missing }\n"
	src2 := "output \"x\" { value = local.ghost }\n"

	f1, _ := parser.ParseFile([]byte(src1), "a.tf")
	f2, _ := parser.ParseFile([]byte(src2), "b.tf")
	errs := analysis.AnalyseFiles([]*ast.File{f1, f2}).Validate()

	refs := make(map[string]bool)
	for _, e := range errs {
		refs[e.Ref] = true
	}
	if !refs["variable.missing"] {
		t.Error("variable.missing should be flagged")
	}
	if !refs["local.ghost"] {
		t.Error("local.ghost should be flagged")
	}
}
