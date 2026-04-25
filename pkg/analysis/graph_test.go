package analysis_test

import (
	"github.com/dgr237/tflens/pkg/analysis"
	"strings"
	"testing"
)

func entityIDs(entities []analysis.Entity) []string {
	ids := make([]string, len(entities))
	for i, e := range entities {
		ids[i] = e.ID()
	}
	return ids
}

func joinIDs(entities []analysis.Entity) string {
	return strings.Join(entityIDs(entities), " ")
}

// ---- cycle detection ----

func TestNoCycles(t *testing.T) {
	src := `
variable "env" {}
locals { prefix = var.env }
resource "aws_vpc" "main" { tags = { Env = local.prefix } }
`
	m := analyseFixture(t, src)
	if cycles := m.Cycles(); len(cycles) > 0 {
		t.Errorf("expected no cycles, got: %v", cycles)
	}
}

func TestDirectCycle(t *testing.T) {
	src := `
locals {
  a = local.b
  b = local.a
}
`
	m := analyseFixture(t, src)
	cycles := m.Cycles()
	if len(cycles) == 0 {
		t.Fatal("expected a cycle, got none")
	}
	joined := strings.Join(cycles[0], " ")
	if !strings.Contains(joined, "local.a") || !strings.Contains(joined, "local.b") {
		t.Errorf("cycle does not contain expected nodes: %v", cycles[0])
	}
	first, last := cycles[0][0], cycles[0][len(cycles[0])-1]
	if first != last {
		t.Errorf("cycle not closed: first=%q last=%q", first, last)
	}
}

func TestLongerCycle(t *testing.T) {
	src := `
locals {
  x = local.z
  y = local.x
  z = local.y
}
`
	m := analyseFixture(t, src)
	if cycles := m.Cycles(); len(cycles) == 0 {
		t.Fatal("expected a cycle, got none")
	}
}

// ---- topological sort ----

func TestTopoSortLinearChain(t *testing.T) {
	src := `
variable "env" {}
locals {
  a = var.env
  b = local.a
  c = local.b
}
`
	m := analyseFixture(t, src)
	sorted, err := m.TopoSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pos := func(id string) int {
		for i, e := range sorted {
			if e.ID() == id {
				return i
			}
		}
		return -1
	}

	for _, pair := range [][2]string{
		{"variable.env", "local.a"},
		{"local.a", "local.b"},
		{"local.b", "local.c"},
	} {
		if pos(pair[0]) >= pos(pair[1]) {
			t.Errorf("%s should appear before %s in topo order", pair[0], pair[1])
		}
	}
}

func TestTopoSortDiamond(t *testing.T) {
	//      var.env
	//      /     \
	//  local.a   local.b
	//      \     /
	//      local.c
	src := `
variable "env" {}
locals {
  a = var.env
  b = var.env
  c = "${local.a}-${local.b}"
}
`
	m := analyseFixture(t, src)
	sorted, err := m.TopoSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	pos := func(id string) int {
		for i, e := range sorted {
			if e.ID() == id {
				return i
			}
		}
		return -1
	}

	for _, pair := range [][2]string{
		{"variable.env", "local.a"},
		{"variable.env", "local.b"},
		{"local.a", "local.c"},
		{"local.b", "local.c"},
	} {
		if pos(pair[0]) >= pos(pair[1]) {
			t.Errorf("%s should appear before %s in topo order", pair[0], pair[1])
		}
	}
}

func TestTopoSortReturnsErrorOnCycle(t *testing.T) {
	src := `
locals {
  a = local.b
  b = local.a
}
`
	m := analyseFixture(t, src)
	if _, err := m.TopoSort(); err == nil {
		t.Error("expected an error for cyclic graph, got nil")
	}
}

func TestTopoSortAllEntitiesPresent(t *testing.T) {
	src := `
variable "env" {}
variable "count" {}
locals { prefix = var.env }
resource "aws_vpc" "main" { tags = { Env = local.prefix } }
output "id" { value = aws_vpc.main.id }
`
	m := analyseFixture(t, src)
	sorted, err := m.TopoSort()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sorted) != len(m.Entities()) {
		t.Errorf("sorted count %d != entity count %d", len(sorted), len(m.Entities()))
	}
}

// ---- impact analysis ----

func TestImpactLeafHasNoImpact(t *testing.T) {
	src := `
resource "aws_vpc" "main" {}
output "id" { value = aws_vpc.main.id }
`
	m := analyseFixture(t, src)
	// output.id has no dependents — nothing is affected by changing it
	if got := m.Impact("output.id"); len(got) != 0 {
		t.Errorf("expected no impact for output.id, got: %v", got)
	}
}

func TestImpactDirectOnly(t *testing.T) {
	src := `
variable "env" {}
locals { prefix = var.env }
`
	m := analyseFixture(t, src)
	impact := m.Impact("variable.env")
	if len(impact) != 1 || impact[0] != "local.prefix" {
		t.Errorf("impact of variable.env: got %v, want [local.prefix]", impact)
	}
}

func TestImpactTransitive(t *testing.T) {
	// var.env → local.prefix → resource.aws_vpc.main → output.id
	src := `
variable "env" {}
locals { prefix = var.env }
resource "aws_vpc" "main" { tags = { Env = local.prefix } }
output "id" { value = aws_vpc.main.id }
`
	m := analyseFixture(t, src)
	impact := m.Impact("variable.env")

	wantAll := map[string]bool{
		"local.prefix":          true,
		"resource.aws_vpc.main": true,
		"output.id":             true,
	}
	for _, id := range impact {
		delete(wantAll, id)
	}
	if len(wantAll) > 0 {
		t.Errorf("impact missing entities: %v (got: %v)", wantAll, impact)
	}
}

func TestImpactTopoOrder(t *testing.T) {
	// Entities in the impact list should appear in dependency order
	// (local.prefix before resource.aws_vpc.main before output.id).
	src := `
variable "env" {}
locals { prefix = var.env }
resource "aws_vpc" "main" { tags = { Env = local.prefix } }
output "id" { value = aws_vpc.main.id }
`
	m := analyseFixture(t, src)
	impact := m.Impact("variable.env")

	pos := func(id string) int {
		for i, s := range impact {
			if s == id {
				return i
			}
		}
		return -1
	}
	pairs := [][2]string{
		{"local.prefix", "resource.aws_vpc.main"},
		{"resource.aws_vpc.main", "output.id"},
	}
	for _, p := range pairs {
		if pos(p[0]) >= pos(p[1]) {
			t.Errorf("impact: %s should appear before %s", p[0], p[1])
		}
	}
}

func TestImpactDiamond(t *testing.T) {
	// var.env fans out to two locals that both feed one resource.
	// The resource should appear once, not twice.
	src := `
variable "env" {}
locals {
  a = var.env
  b = var.env
}
resource "aws_vpc" "main" { tags = { A = local.a, B = local.b } }
`
	m := analyseFixture(t, src)
	impact := m.Impact("variable.env")

	seen := make(map[string]int)
	for _, id := range impact {
		seen[id]++
	}
	for id, count := range seen {
		if count > 1 {
			t.Errorf("impact: %s appears %d times, want 1", id, count)
		}
	}
	if _, ok := seen["resource.aws_vpc.main"]; !ok {
		t.Error("impact: resource.aws_vpc.main should be included")
	}
}

// ---- unreferenced detection ----

func TestUnreferencedVariable(t *testing.T) {
	src := `
variable "used" {}
variable "unused" {}
locals { x = var.used }
`
	m := analyseFixture(t, src)
	joined := joinIDs(m.Unreferenced())
	if !strings.Contains(joined, "variable.unused") {
		t.Errorf("expected variable.unused in unreferenced list, got: %q", joined)
	}
	if strings.Contains(joined, "variable.used") {
		t.Errorf("variable.used should NOT appear in unreferenced list")
	}
}

func TestUnreferencedLocal(t *testing.T) {
	src := `
variable "env" {}
locals {
  used   = var.env
  unused = "dead"
}
resource "aws_vpc" "main" { tags = { Env = local.used } }
`
	m := analyseFixture(t, src)
	joined := joinIDs(m.Unreferenced())
	if !strings.Contains(joined, "local.unused") {
		t.Errorf("expected local.unused in unreferenced list, got: %q", joined)
	}
	if strings.Contains(joined, "local.used") {
		t.Errorf("local.used should NOT appear in unreferenced list")
	}
}

func TestOutputsExcludedFromUnreferenced(t *testing.T) {
	src := `
resource "aws_vpc" "main" {}
output "id" { value = aws_vpc.main.id }
`
	m := analyseFixture(t, src)
	for _, e := range m.Unreferenced() {
		if e.ID() == "output.id" {
			t.Error("outputs should never appear in the unreferenced list")
		}
	}
}

func TestNoUnreferencedInTightGraph(t *testing.T) {
	src := `
variable "env" {}
locals { prefix = var.env }
resource "aws_vpc" "main" { tags = { Env = local.prefix } }
output "id" { value = aws_vpc.main.id }
`
	m := analyseFixture(t, src)
	if refs := m.Unreferenced(); len(refs) != 0 {
		t.Errorf("expected no unreferenced entities, got: %v", entityIDs(refs))
	}
}

func TestUnreferencedDataSource(t *testing.T) {
	src := `
data "aws_ami" "ubuntu" { most_recent = true }
data "aws_ami" "windows" { most_recent = true }
resource "aws_instance" "web" { ami = data.aws_ami.ubuntu.id }
`
	m := analyseFixture(t, src)
	joined := joinIDs(m.Unreferenced())
	if !strings.Contains(joined, "data.aws_ami.windows") {
		t.Errorf("expected unused data source in unreferenced list, got: %q", joined)
	}
	if strings.Contains(joined, "data.aws_ami.ubuntu") {
		t.Errorf("data.aws_ami.ubuntu should NOT appear in unreferenced list")
	}
}
