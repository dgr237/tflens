package analysis_test

import (
	"strings"
	"testing"
	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/parser"
)

// analyseForTypes parses src and returns the module so tests can inspect both
// entities and type errors.
func analyseForTypes(t *testing.T, src string) *analysis.Module {
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

// ---- ParseType ----

func TestParseTypePrimitives(t *testing.T) {
	cases := []struct {
		src  string
		want analysis.TypeKind
	}{
		{`variable "x" { type = string }`, analysis.TypeString},
		{`variable "x" { type = number }`, analysis.TypeNumber},
		{`variable "x" { type = bool }`, analysis.TypeBool},
		{`variable "x" { type = any }`, analysis.TypeAny},
	}
	for _, c := range cases {
		m := analyseForTypes(t, c.src)
		vars := m.Filter(analysis.KindVariable)
		if len(vars) != 1 {
			t.Fatalf("%s: expected 1 variable", c.src)
		}
		if vars[0].DeclaredType == nil || vars[0].DeclaredType.Kind != c.want {
			t.Errorf("%s: got %v, want kind %v", c.src, vars[0].DeclaredType, c.want)
		}
	}
}

func TestParseTypeParameterised(t *testing.T) {
	m := analyseForTypes(t, `
variable "names" { type = list(string) }
variable "tags"  { type = map(string) }
variable "ids"   { type = set(number) }
`)
	got := map[string]string{}
	for _, e := range m.Filter(analysis.KindVariable) {
		got[e.Name] = e.DeclaredType.String()
	}
	if got["names"] != "list(string)" {
		t.Errorf("names: got %q, want list(string)", got["names"])
	}
	if got["tags"] != "map(string)" {
		t.Errorf("tags: got %q, want map(string)", got["tags"])
	}
	if got["ids"] != "set(number)" {
		t.Errorf("ids: got %q, want set(number)", got["ids"])
	}
}

func TestParseTypeObject(t *testing.T) {
	m := analyseForTypes(t, `
variable "cfg" {
  type = object({
    name = string
    port = number
  })
}
`)
	v := m.Filter(analysis.KindVariable)[0]
	if v.DeclaredType.Kind != analysis.TypeObject {
		t.Fatalf("kind = %v, want Object", v.DeclaredType.Kind)
	}
	if v.DeclaredType.Fields["name"].Kind != analysis.TypeString {
		t.Errorf("name field: got %v, want string", v.DeclaredType.Fields["name"])
	}
	if v.DeclaredType.Fields["port"].Kind != analysis.TypeNumber {
		t.Errorf("port field: got %v, want number", v.DeclaredType.Fields["port"])
	}
}

// ---- default value checking ----

func TestDefaultMatchesType(t *testing.T) {
	m := analyseForTypes(t, `
variable "env" {
  type    = string
  default = "prod"
}
variable "count" {
  type    = number
  default = 3
}
variable "enabled" {
  type    = bool
  default = true
}
`)
	if errs := m.TypeErrors(); len(errs) != 0 {
		t.Errorf("expected no type errors, got: %v", errs)
	}
}

func TestDefaultTypeMismatch(t *testing.T) {
	m := analyseForTypes(t, `
variable "count" {
  type    = number
  default = "three"
}
`)
	errs := m.TypeErrors()
	if len(errs) != 1 {
		t.Fatalf("expected 1 type error, got %d: %v", len(errs), errs)
	}
	if errs[0].Attr != "default" {
		t.Errorf("Attr = %q, want %q", errs[0].Attr, "default")
	}
	if !strings.Contains(errs[0].Msg, "variable.count") {
		t.Errorf("message should mention variable.count: %s", errs[0].Msg)
	}
	if !strings.Contains(errs[0].Msg, "string") || !strings.Contains(errs[0].Msg, "number") {
		t.Errorf("message should mention both types: %s", errs[0].Msg)
	}
}

func TestDefaultNullAlwaysOK(t *testing.T) {
	// null is a universal value — it should not cause type errors.
	m := analyseForTypes(t, `
variable "x" {
  type    = string
  default = null
}
`)
	if errs := m.TypeErrors(); len(errs) != 0 {
		t.Errorf("null default should not produce errors, got: %v", errs)
	}
}

func TestDefaultAnyAcceptsAnything(t *testing.T) {
	m := analyseForTypes(t, `
variable "a" {
  type    = any
  default = "string"
}
variable "b" {
  type    = any
  default = 42
}
variable "c" {
  type    = any
  default = true
}
`)
	if errs := m.TypeErrors(); len(errs) != 0 {
		t.Errorf("any should accept everything, got: %v", errs)
	}
}

// ---- for_each ----

func TestForEachWithListLiteralFails(t *testing.T) {
	src := `
resource "aws_subnet" "pub" {
  for_each = ["a", "b", "c"]
}
`
	m := analyseForTypes(t, src)
	errs := m.TypeErrors()
	if len(errs) == 0 {
		t.Fatal("expected a type error for list for_each, got none")
	}
	if errs[0].Attr != "for_each" {
		t.Errorf("Attr = %q, want for_each", errs[0].Attr)
	}
	if !strings.Contains(errs[0].Msg, "map") || !strings.Contains(errs[0].Msg, "set") {
		t.Errorf("error should mention map and set: %s", errs[0].Msg)
	}
}

func TestForEachWithMapLiteralOK(t *testing.T) {
	src := `
resource "aws_subnet" "pub" {
  for_each = { a = 1, b = 2 }
}
`
	if errs := analyseForTypes(t, src).TypeErrors(); len(errs) != 0 {
		t.Errorf("map literal for_each should be OK, got: %v", errs)
	}
}

func TestForEachWithTosetOK(t *testing.T) {
	src := `
variable "names" { type = list(string) }
resource "aws_iam_user" "u" {
  for_each = toset(var.names)
}
`
	if errs := analyseForTypes(t, src).TypeErrors(); len(errs) != 0 {
		t.Errorf("toset() for_each should be OK, got: %v", errs)
	}
}

func TestForEachWithListVarFails(t *testing.T) {
	// var.names is declared list → passing it to for_each without toset fails.
	src := `
variable "names" { type = list(string) }
resource "aws_iam_user" "u" {
  for_each = var.names
}
`
	errs := analyseForTypes(t, src).TypeErrors()
	if len(errs) == 0 {
		t.Fatal("expected a type error for list-typed var in for_each, got none")
	}
	if !strings.Contains(errs[0].Msg, "list") {
		t.Errorf("error should mention list: %s", errs[0].Msg)
	}
}

func TestForEachWithMapVarOK(t *testing.T) {
	src := `
variable "tags" { type = map(string) }
resource "aws_instance" "w" {
  for_each = var.tags
}
`
	if errs := analyseForTypes(t, src).TypeErrors(); len(errs) != 0 {
		t.Errorf("map-typed var in for_each should be OK, got: %v", errs)
	}
}

func TestForEachWithStringLiteralFails(t *testing.T) {
	// string is not iterable for for_each.
	src := `
resource "aws_iam_user" "u" {
  for_each = "not-a-set"
}
`
	if errs := analyseForTypes(t, src).TypeErrors(); len(errs) == 0 {
		t.Error("expected a type error for string for_each, got none")
	}
}

func TestForEachInModuleBlock(t *testing.T) {
	// for_each applies to module blocks too.
	src := `
module "envs" {
  source   = "./envs"
  for_each = [1, 2, 3]
}
`
	errs := analyseForTypes(t, src).TypeErrors()
	if len(errs) == 0 {
		t.Fatal("expected a type error for list for_each in module, got none")
	}
	if errs[0].EntityID != "module.envs" {
		t.Errorf("EntityID = %q, want module.envs", errs[0].EntityID)
	}
}

// ---- count ----

func TestCountWithNumberOK(t *testing.T) {
	src := `resource "aws_instance" "w" { count = 3 }`
	if errs := analyseForTypes(t, src).TypeErrors(); len(errs) != 0 {
		t.Errorf("count = 3 should be OK, got: %v", errs)
	}
}

func TestCountWithListFails(t *testing.T) {
	src := `resource "aws_instance" "w" { count = ["a", "b"] }`
	errs := analyseForTypes(t, src).TypeErrors()
	if len(errs) == 0 {
		t.Fatal("expected a type error for list count, got none")
	}
	if errs[0].Attr != "count" {
		t.Errorf("Attr = %q, want count", errs[0].Attr)
	}
}

func TestCountWithBoolFails(t *testing.T) {
	src := `resource "aws_instance" "w" { count = true }`
	if errs := analyseForTypes(t, src).TypeErrors(); len(errs) == 0 {
		t.Error("expected a type error for count = true, got none")
	}
}

func TestCountWithNumberVarOK(t *testing.T) {
	src := `
variable "n" { type = number }
resource "aws_instance" "w" { count = var.n }
`
	if errs := analyseForTypes(t, src).TypeErrors(); len(errs) != 0 {
		t.Errorf("count = var.n (number) should be OK, got: %v", errs)
	}
}

// ---- edge cases ----

func TestTypeErrorsSortedByPosition(t *testing.T) {
	src := `
variable "a" {
  type    = number
  default = "x"
}
variable "b" {
  type    = bool
  default = 1
}
`
	errs := analyseForTypes(t, src).TypeErrors()
	if len(errs) < 2 {
		t.Fatalf("expected 2 errors, got %d", len(errs))
	}
	if errs[0].Pos.Line >= errs[1].Pos.Line {
		t.Errorf("errors not sorted: got lines %d, %d", errs[0].Pos.Line, errs[1].Pos.Line)
	}
}

func TestUnknownTypeConstraintTolerated(t *testing.T) {
	// Variables without a type constraint get no DeclaredType; don't crash.
	m := analyseForTypes(t, `variable "x" { default = "hi" }`)
	if errs := m.TypeErrors(); len(errs) != 0 {
		t.Errorf("variable without type constraint should produce no errors, got: %v", errs)
	}
}

func TestForEachUnknownTypeIsAllowed(t *testing.T) {
	// When we can't infer a type, we conservatively allow it.
	src := `
resource "aws_instance" "w" {
  for_each = some_function(arg)
}
`
	if errs := analyseForTypes(t, src).TypeErrors(); len(errs) != 0 {
		t.Errorf("unknown-type for_each should be tolerated, got: %v", errs)
	}
}

// ---- built-in function return types ----

func TestForEachWithKeysFails(t *testing.T) {
	// `keys(map)` returns list(string) — the classic for_each mistake.
	src := `
variable "tags" { type = map(string) }
resource "aws_instance" "w" {
  for_each = keys(var.tags)
}
`
	errs := analyseForTypes(t, src).TypeErrors()
	if len(errs) == 0 {
		t.Fatal("expected error for for_each = keys(...), got none")
	}
	if !strings.Contains(errs[0].Msg, "list") {
		t.Errorf("error should mention list: %s", errs[0].Msg)
	}
}

func TestForEachWithValuesFails(t *testing.T) {
	// `values(map)` returns list.
	src := `
variable "tags" { type = map(string) }
resource "aws_instance" "w" {
  for_each = values(var.tags)
}
`
	if errs := analyseForTypes(t, src).TypeErrors(); len(errs) == 0 {
		t.Error("expected error for for_each = values(...), got none")
	}
}

func TestForEachWithConcatFails(t *testing.T) {
	src := `
resource "aws_instance" "w" {
  for_each = concat(["a"], ["b"])
}
`
	if errs := analyseForTypes(t, src).TypeErrors(); len(errs) == 0 {
		t.Error("expected error for for_each = concat(...), got none")
	}
}

func TestForEachWithMergeOK(t *testing.T) {
	// merge returns map — valid for_each.
	src := `
variable "a" { type = map(string) }
variable "b" { type = map(string) }
resource "aws_instance" "w" {
  for_each = merge(var.a, var.b)
}
`
	if errs := analyseForTypes(t, src).TypeErrors(); len(errs) != 0 {
		t.Errorf("merge() for_each should be OK, got: %v", errs)
	}
}

func TestForEachWithFilesetOK(t *testing.T) {
	// fileset returns set — valid for_each.
	src := `
resource "aws_s3_object" "files" {
  for_each = fileset("./", "*.txt")
}
`
	if errs := analyseForTypes(t, src).TypeErrors(); len(errs) != 0 {
		t.Errorf("fileset() for_each should be OK, got: %v", errs)
	}
}

func TestCountWithLengthOK(t *testing.T) {
	// count = length(var.names) is a valid idiom.
	src := `
variable "names" { type = list(string) }
resource "aws_iam_user" "u" {
  count = length(var.names)
}
`
	if errs := analyseForTypes(t, src).TypeErrors(); len(errs) != 0 {
		t.Errorf("count = length(...) should be OK, got: %v", errs)
	}
}

func TestCountWithKeysFails(t *testing.T) {
	// count = keys(var.tags) is a list, not a number.
	src := `
variable "tags" { type = map(string) }
resource "aws_instance" "w" {
  count = keys(var.tags)
}
`
	if errs := analyseForTypes(t, src).TypeErrors(); len(errs) == 0 {
		t.Error("expected error for count = keys(...), got none")
	}
}

func TestDefaultWithJsonencodeOK(t *testing.T) {
	// jsonencode returns string.
	src := `
variable "cfg" {
  type    = string
  default = jsonencode({ a = 1 })
}
`
	if errs := analyseForTypes(t, src).TypeErrors(); len(errs) != 0 {
		t.Errorf("jsonencode default should satisfy string type, got: %v", errs)
	}
}

func TestDefaultWithWrongBuiltinFails(t *testing.T) {
	// length returns number, can't satisfy string type.
	src := `
variable "name" {
  type    = string
  default = length("hello")
}
`
	errs := analyseForTypes(t, src).TypeErrors()
	if len(errs) == 0 {
		t.Fatal("expected error for default = length(...) with string type, got none")
	}
	if !strings.Contains(errs[0].Msg, "number") || !strings.Contains(errs[0].Msg, "string") {
		t.Errorf("error should mention number and string: %s", errs[0].Msg)
	}
}
