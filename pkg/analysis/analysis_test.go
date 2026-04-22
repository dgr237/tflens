package analysis_test

import (
	"testing"
	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/parser"
)

func analyseFixture(t *testing.T, src string) *analysis.Module {
	t.Helper()
	file, errs := parser.ParseFile([]byte(src), "test.tf")
	for _, e := range errs {
		t.Errorf("parse error: %s", e)
	}
	if t.Failed() {
		t.FailNow()
	}
	return analysis.Analyse(file)
}

// ---- entity inventory ----

func TestEntityCounts(t *testing.T) {
	src := `
variable "env" { type = string }
variable "count" { type = number }

locals {
  prefix = "${var.env}-app"
  is_prod = var.env == "prod"
}

data "aws_ami" "ubuntu" { most_recent = true }

resource "aws_vpc" "main" { cidr_block = "10.0.0.0/16" }
resource "aws_instance" "web" { ami = data.aws_ami.ubuntu.id }

output "vpc_id" { value = aws_vpc.main.id }
`
	m := analyseFixture(t, src)

	check := func(kind analysis.EntityKind, want int) {
		t.Helper()
		got := len(m.Filter(kind))
		if got != want {
			t.Errorf("%s count: got %d, want %d", kind, got, want)
		}
	}
	check(analysis.KindVariable, 2)
	check(analysis.KindLocal, 2)
	check(analysis.KindData, 1)
	check(analysis.KindResource, 2)
	check(analysis.KindOutput, 1)
}

func TestEntityIDs(t *testing.T) {
	src := `
variable "env" {}
data "aws_ami" "ubuntu" {}
resource "aws_instance" "web" {}
output "id" { value = aws_instance.web.id }
`
	m := analyseFixture(t, src)
	wantIDs := map[string]bool{
		"variable.env":              true,
		"data.aws_ami.ubuntu":       true,
		"resource.aws_instance.web": true,
		"output.id":                 true,
	}
	for _, e := range m.Entities() {
		delete(wantIDs, e.ID())
	}
	for id := range wantIDs {
		t.Errorf("missing entity: %s", id)
	}
}

// ---- dependency edges ----

func TestVarDependency(t *testing.T) {
	src := `
variable "env" {}
locals { prefix = var.env }
`
	m := analyseFixture(t, src)
	if !m.HasDep("local.prefix", "variable.env") {
		t.Error("local.prefix should depend on variable.env")
	}
}

func TestLocalToLocalDependency(t *testing.T) {
	src := `
variable "env" {}
locals {
  is_prod = var.env == "prod"
  count   = local.is_prod ? 2 : 1
}
`
	m := analyseFixture(t, src)
	if !m.HasDep("local.count", "local.is_prod") {
		t.Error("local.count should depend on local.is_prod")
	}
}

func TestResourceToDataDependency(t *testing.T) {
	src := `
data "aws_ami" "ubuntu" { most_recent = true }
resource "aws_instance" "web" { ami = data.aws_ami.ubuntu.id }
`
	m := analyseFixture(t, src)
	if !m.HasDep("resource.aws_instance.web", "data.aws_ami.ubuntu") {
		t.Error("aws_instance.web should depend on data.aws_ami.ubuntu")
	}
}

func TestResourceToResourceDependency(t *testing.T) {
	src := `
resource "aws_vpc" "main" { cidr_block = "10.0.0.0/16" }
resource "aws_subnet" "pub" { vpc_id = aws_vpc.main.id }
`
	m := analyseFixture(t, src)
	if !m.HasDep("resource.aws_subnet.pub", "resource.aws_vpc.main") {
		t.Error("aws_subnet.pub should depend on aws_vpc.main")
	}
}

func TestOutputDependency(t *testing.T) {
	src := `
resource "aws_vpc" "main" { cidr_block = "10.0.0.0/16" }
output "vpc_id" { value = aws_vpc.main.id }
`
	m := analyseFixture(t, src)
	if !m.HasDep("output.vpc_id", "resource.aws_vpc.main") {
		t.Error("output.vpc_id should depend on resource.aws_vpc.main")
	}
}

func TestDepInNestedBlock(t *testing.T) {
	// References inside nested blocks (e.g. ingress {}) still count.
	src := `
resource "aws_vpc" "main" {}
resource "aws_security_group" "web" {
  vpc_id = aws_vpc.main.id
  ingress {
    cidr_blocks = [aws_vpc.main.cidr_block]
  }
}
`
	m := analyseFixture(t, src)
	if !m.HasDep("resource.aws_security_group.web", "resource.aws_vpc.main") {
		t.Error("aws_security_group.web should depend on aws_vpc.main (via nested block)")
	}
}

func TestDepInTemplateString(t *testing.T) {
	src := `
variable "env" {}
locals { name = "${var.env}-app" }
`
	m := analyseFixture(t, src)
	if !m.HasDep("local.name", "variable.env") {
		t.Error("local.name should depend on variable.env (via template)")
	}
}

func TestDepInForExpr(t *testing.T) {
	src := `
resource "aws_instance" "web" {}
locals { ids = [for i in aws_instance.web : i.id] }
`
	m := analyseFixture(t, src)
	if !m.HasDep("local.ids", "resource.aws_instance.web") {
		t.Error("local.ids should depend on resource.aws_instance.web (via for expr)")
	}
}

func TestNoDepsForUnknownRef(t *testing.T) {
	// count.index and each.key are built-ins, not declared entities — no edges.
	src := `
resource "aws_subnet" "pub" {
  count      = 3
  cidr_block = cidrsubnet("10.0.0.0/16", 8, count.index)
}
`
	m := analyseFixture(t, src)
	deps := m.Dependencies("resource.aws_subnet.pub")
	for _, d := range deps {
		if d == "count.index" {
			t.Error("count.index should not appear as a dependency")
		}
	}
}

func TestDependents(t *testing.T) {
	src := `
resource "aws_vpc" "main" {}
resource "aws_subnet" "pub" { vpc_id = aws_vpc.main.id }
resource "aws_security_group" "web" { vpc_id = aws_vpc.main.id }
`
	m := analyseFixture(t, src)
	deps := m.Dependents("resource.aws_vpc.main")
	wantDeps := map[string]bool{
		"resource.aws_subnet.pub":         true,
		"resource.aws_security_group.web": true,
	}
	for _, d := range deps {
		delete(wantDeps, d)
	}
	if len(wantDeps) > 0 {
		t.Errorf("missing dependents: %v", wantDeps)
	}
}

// ---- DOT output sanity check ----

func TestToDOTContainsNodes(t *testing.T) {
	src := `
variable "env" {}
resource "aws_vpc" "main" { tags = { Env = var.env } }
`
	m := analyseFixture(t, src)
	dot := m.ToDOT()
	for _, want := range []string{"variable.env", "resource.aws_vpc.main", "->"} {
		if !containsStr(dot, want) {
			t.Errorf("DOT output missing %q:\n%s", want, dot)
		}
	}
}

func containsStr(s, sub string) bool {
	return len(s) >= len(sub) && (s == sub || len(s) > 0 && stringContains(s, sub))
}

func stringContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// ---- source locations ----

func TestEntityPositionFile(t *testing.T) {
	// The file name passed to ParseFile should appear in every entity's location.
	src := `
variable "env" {}
resource "aws_vpc" "main" {}
`
	file, errs := parser.ParseFile([]byte(src), "infra.tf")
	for _, e := range errs {
		t.Errorf("parse error: %s", e)
	}
	m := analysis.Analyse(file)
	for _, e := range m.Entities() {
		if e.Pos.File != "infra.tf" {
			t.Errorf("entity %s: Pos.File = %q, want %q", e.ID(), e.Pos.File, "infra.tf")
		}
		if loc := e.Location(); loc == "" {
			t.Errorf("entity %s: Location() returned empty string", e.ID())
		}
	}
}

func TestEntityPositionLine(t *testing.T) {
	// Each entity's line number should match its position in the source.
	// Leading blank line means declarations start at line 2.
	src := "\nvariable \"a\" {}\nvariable \"b\" {}\nlocals {\n  x = 1\n  y = 2\n}\nresource \"aws_vpc\" \"main\" {}\noutput \"id\" { value = aws_vpc.main.id }\n"
	file, errs := parser.ParseFile([]byte(src), "test.tf")
	for _, e := range errs {
		t.Errorf("parse error: %s", e)
	}
	m := analysis.Analyse(file)

	byID := make(map[string]int)
	for _, e := range m.Entities() {
		byID[e.ID()] = e.Pos.Line
	}

	cases := []struct {
		id   string
		line int
	}{
		{"variable.a", 2},
		{"variable.b", 3},
		{"local.x", 5},
		{"local.y", 6},
		{"resource.aws_vpc.main", 8},
		{"output.id", 9},
	}
	for _, c := range cases {
		if got, ok := byID[c.id]; !ok {
			t.Errorf("entity %s not found", c.id)
		} else if got != c.line {
			t.Errorf("entity %s: line %d, want %d", c.id, got, c.line)
		}
	}
}

func TestLocalPositionIsAttributeNotBlock(t *testing.T) {
	// Locals live inside a `locals {}` block; each local's position should
	// point to the individual attribute line, not the block's opening line.
	src := "locals {\n  a = 1\n  b = 2\n}\n"
	file, errs := parser.ParseFile([]byte(src), "test.tf")
	for _, e := range errs {
		t.Errorf("parse error: %s", e)
	}
	m := analysis.Analyse(file)

	lineA, lineB := 0, 0
	for _, e := range m.Entities() {
		switch e.ID() {
		case "local.a":
			lineA = e.Pos.Line
		case "local.b":
			lineB = e.Pos.Line
		}
	}
	if lineA != 2 {
		t.Errorf("local.a: line %d, want 2", lineA)
	}
	if lineB != 3 {
		t.Errorf("local.b: line %d, want 3", lineB)
	}
}

func TestLocationMethod(t *testing.T) {
	src := "variable \"env\" {}\n"
	file, errs := parser.ParseFile([]byte(src), "/some/path/variables.tf")
	for _, e := range errs {
		t.Errorf("parse error: %s", e)
	}
	m := analysis.Analyse(file)
	entities := m.Filter(analysis.KindVariable)
	if len(entities) != 1 {
		t.Fatalf("expected 1 variable, got %d", len(entities))
	}
	loc := entities[0].Location()
	if loc != "variables.tf:1" {
		t.Errorf("Location() = %q, want %q", loc, "variables.tf:1")
	}
}
