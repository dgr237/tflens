package parser_test

import (
	"testing"
	"github.com/dgr237/tflens/pkg/ast"
	"github.com/dgr237/tflens/pkg/parser"
)

func mustParse(t *testing.T, src string) *ast.File {
	t.Helper()
	file, errs := parser.ParseFile([]byte(src), "test.tf")
	if len(errs) > 0 {
		for _, e := range errs {
			t.Errorf("parse error: %s", e)
		}
		t.FailNow()
	}
	return file
}

func TestParseSimpleAttribute(t *testing.T) {
	file := mustParse(t, `name = "world"`)
	if len(file.Body.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(file.Body.Nodes))
	}
	attr, ok := file.Body.Nodes[0].(*ast.Attribute)
	if !ok {
		t.Fatalf("expected *ast.Attribute, got %T", file.Body.Nodes[0])
	}
	if attr.Name != "name" {
		t.Errorf("name: got %q, want %q", attr.Name, "name")
	}
	lit, ok := attr.Value.(*ast.LiteralExpr)
	if !ok {
		t.Fatalf("expected *ast.LiteralExpr, got %T", attr.Value)
	}
	if lit.Value != "world" {
		t.Errorf("value: got %v, want %q", lit.Value, "world")
	}
}

func TestParseBlock(t *testing.T) {
	src := `
resource "aws_instance" "web" {
  ami = "ami-123"
}
`
	file := mustParse(t, src)
	if len(file.Body.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(file.Body.Nodes))
	}
	block, ok := file.Body.Nodes[0].(*ast.Block)
	if !ok {
		t.Fatalf("expected *ast.Block, got %T", file.Body.Nodes[0])
	}
	if block.Type != "resource" {
		t.Errorf("block type: got %q, want %q", block.Type, "resource")
	}
	if len(block.Labels) != 2 || block.Labels[0] != "aws_instance" || block.Labels[1] != "web" {
		t.Errorf("labels: got %v", block.Labels)
	}
	if len(block.Body.Nodes) != 1 {
		t.Errorf("body: expected 1 node, got %d", len(block.Body.Nodes))
	}
}

func TestParseNestedBlocks(t *testing.T) {
	src := `
terraform {
  required_providers {
    aws = {
      source  = "hashicorp/aws"
      version = "~> 4.0"
    }
  }
}
`
	file := mustParse(t, src)
	if len(file.Body.Nodes) != 1 {
		t.Fatalf("expected 1 node, got %d", len(file.Body.Nodes))
	}
}

func TestParseBoolAndNull(t *testing.T) {
	file := mustParse(t, "enabled = true\ndisabled = false\nnothing = null")
	if len(file.Body.Nodes) != 3 {
		t.Fatalf("expected 3 nodes, got %d", len(file.Body.Nodes))
	}
	checkLiteral := func(idx int, want any) {
		t.Helper()
		attr := file.Body.Nodes[idx].(*ast.Attribute)
		lit := attr.Value.(*ast.LiteralExpr)
		if lit.Value != want {
			t.Errorf("node[%d]: got %v, want %v", idx, lit.Value, want)
		}
	}
	checkLiteral(0, true)
	checkLiteral(1, false)
	checkLiteral(2, nil)
}

func TestParseReference(t *testing.T) {
	file := mustParse(t, `id = aws_instance.web.id`)
	attr := file.Body.Nodes[0].(*ast.Attribute)
	ref, ok := attr.Value.(*ast.RefExpr)
	if !ok {
		t.Fatalf("expected *ast.RefExpr, got %T", attr.Value)
	}
	want := []string{"aws_instance", "web", "id"}
	if len(ref.Parts) != len(want) {
		t.Fatalf("parts: got %v, want %v", ref.Parts, want)
	}
	for i, p := range want {
		if ref.Parts[i] != p {
			t.Errorf("part[%d]: got %q, want %q", i, ref.Parts[i], p)
		}
	}
}

func TestParseFunctionCall(t *testing.T) {
	file := mustParse(t, `x = tostring(42)`)
	attr := file.Body.Nodes[0].(*ast.Attribute)
	call, ok := attr.Value.(*ast.CallExpr)
	if !ok {
		t.Fatalf("expected *ast.CallExpr, got %T", attr.Value)
	}
	if call.Name != "tostring" {
		t.Errorf("name: got %q, want %q", call.Name, "tostring")
	}
	if len(call.Args) != 1 {
		t.Errorf("args: got %d, want 1", len(call.Args))
	}
}

func TestParseTuple(t *testing.T) {
	file := mustParse(t, `cidrs = ["10.0.0.0/8", "172.16.0.0/12"]`)
	attr := file.Body.Nodes[0].(*ast.Attribute)
	tuple, ok := attr.Value.(*ast.TupleExpr)
	if !ok {
		t.Fatalf("expected *ast.TupleExpr, got %T", attr.Value)
	}
	if len(tuple.Items) != 2 {
		t.Errorf("items: got %d, want 2", len(tuple.Items))
	}
}

func TestParseTemplateString(t *testing.T) {
	file := mustParse(t, `name = "hello ${var.name}"`)
	attr := file.Body.Nodes[0].(*ast.Attribute)
	tmpl, ok := attr.Value.(*ast.TemplateExpr)
	if !ok {
		t.Fatalf("expected *ast.TemplateExpr, got %T", attr.Value)
	}
	if len(tmpl.Parts) < 2 {
		t.Errorf("expected at least 2 parts, got %d", len(tmpl.Parts))
	}
}

func TestParseConditional(t *testing.T) {
	file := mustParse(t, `x = enabled ? "yes" : "no"`)
	attr := file.Body.Nodes[0].(*ast.Attribute)
	_, ok := attr.Value.(*ast.CondExpr)
	if !ok {
		t.Fatalf("expected *ast.CondExpr, got %T", attr.Value)
	}
}

func TestParseForListExpr(t *testing.T) {
	file := mustParse(t, `names = [for s in var.list : s.name]`)
	attr := file.Body.Nodes[0].(*ast.Attribute)
	_, ok := attr.Value.(*ast.ForExpr)
	if !ok {
		t.Fatalf("expected *ast.ForExpr, got %T", attr.Value)
	}
}

func TestParseGroupedExpr(t *testing.T) {
	// (1 + 2) * 3  — grouping must override default precedence
	file := mustParse(t, `x = (1 + 2) * 3`)
	attr := file.Body.Nodes[0].(*ast.Attribute)
	outer, ok := attr.Value.(*ast.BinaryExpr)
	if !ok {
		t.Fatalf("expected *ast.BinaryExpr, got %T", attr.Value)
	}
	if outer.Op != "*" {
		t.Errorf("outer op: got %q, want %q (grouping should make + bind tighter)", outer.Op, "*")
	}
	if _, ok := outer.Left.(*ast.BinaryExpr); !ok {
		t.Errorf("left side should be the grouped BinaryExpr (+), got %T", outer.Left)
	}
}

func TestParseBinaryExpr(t *testing.T) {
	file := mustParse(t, `x = 1 + 2 * 3`)
	attr := file.Body.Nodes[0].(*ast.Attribute)
	// Should be: 1 + (2 * 3) due to precedence
	outer, ok := attr.Value.(*ast.BinaryExpr)
	if !ok {
		t.Fatalf("expected *ast.BinaryExpr, got %T", attr.Value)
	}
	if outer.Op != "+" {
		t.Errorf("outer op: got %q, want %q", outer.Op, "+")
	}
	inner, ok := outer.Right.(*ast.BinaryExpr)
	if !ok {
		t.Fatalf("right side: expected *ast.BinaryExpr, got %T", outer.Right)
	}
	if inner.Op != "*" {
		t.Errorf("inner op: got %q, want %q", inner.Op, "*")
	}
}

func TestParseUnaryExpr(t *testing.T) {
	file := mustParse(t, `x = !enabled`)
	attr := file.Body.Nodes[0].(*ast.Attribute)
	unary, ok := attr.Value.(*ast.UnaryExpr)
	if !ok {
		t.Fatalf("expected *ast.UnaryExpr, got %T", attr.Value)
	}
	if unary.Op != "!" {
		t.Errorf("op: got %q, want %q", unary.Op, "!")
	}
}

func TestParseUnaryMinus(t *testing.T) {
	file := mustParse(t, `x = -1`)
	attr := file.Body.Nodes[0].(*ast.Attribute)
	unary, ok := attr.Value.(*ast.UnaryExpr)
	if !ok {
		t.Fatalf("expected *ast.UnaryExpr, got %T", attr.Value)
	}
	if unary.Op != "-" {
		t.Errorf("op: got %q, want %q", unary.Op, "-")
	}
}

func TestParsePrecedenceLeftAssoc(t *testing.T) {
	// a - b - c should parse as (a - b) - c, not a - (b - c)
	file := mustParse(t, `x = a - b - c`)
	attr := file.Body.Nodes[0].(*ast.Attribute)
	outer, ok := attr.Value.(*ast.BinaryExpr)
	if !ok {
		t.Fatalf("expected *ast.BinaryExpr, got %T", attr.Value)
	}
	if _, ok := outer.Left.(*ast.BinaryExpr); !ok {
		t.Errorf("expected left side to be a BinaryExpr (left-associative), got %T", outer.Left)
	}
}

func TestParseLogicalExpr(t *testing.T) {
	file := mustParse(t, `x = a && b || c`)
	attr := file.Body.Nodes[0].(*ast.Attribute)
	// || has lower precedence: (a && b) || c
	outer, ok := attr.Value.(*ast.BinaryExpr)
	if !ok {
		t.Fatalf("expected *ast.BinaryExpr, got %T", attr.Value)
	}
	if outer.Op != "||" {
		t.Errorf("outer op: got %q, want %q", outer.Op, "||")
	}
	if _, ok := outer.Left.(*ast.BinaryExpr); !ok {
		t.Errorf("left side should be a BinaryExpr (&&), got %T", outer.Left)
	}
}

func TestParseComparisonInConditional(t *testing.T) {
	file := mustParse(t, `x = count > 0 ? "yes" : "no"`)
	attr := file.Body.Nodes[0].(*ast.Attribute)
	cond, ok := attr.Value.(*ast.CondExpr)
	if !ok {
		t.Fatalf("expected *ast.CondExpr, got %T", attr.Value)
	}
	if _, ok := cond.Pred.(*ast.BinaryExpr); !ok {
		t.Errorf("pred should be BinaryExpr, got %T", cond.Pred)
	}
}

// ---- error recovery ----

// parseWithErrors is like mustParse but returns errors instead of failing.
func parseWithErrors(t *testing.T, src string) (*ast.File, []parser.ParseError) {
	t.Helper()
	file, errs := parser.ParseFile([]byte(src), "test.tf")
	return file, errs
}

func TestRecoveryBadAttributeDoesNotBlockNext(t *testing.T) {
	// The bad line should produce errors; the good lines should still parse.
	src := "good = 1\n??? bad syntax\nafter = 2\n"
	file, errs := parseWithErrors(t, src)
	if len(errs) == 0 {
		t.Fatal("expected parse errors, got none")
	}
	// Both the good attribute before and the one after the bad line should parse.
	names := map[string]bool{}
	for _, n := range file.Body.Nodes {
		if a, ok := n.(*ast.Attribute); ok {
			names[a.Name] = true
		}
	}
	if !names["good"] {
		t.Error("attribute 'good' before bad line should have been parsed")
	}
	if !names["after"] {
		t.Error("attribute 'after' following bad line should have been parsed")
	}
}

func TestRecoveryBadBlockDoesNotBlockNext(t *testing.T) {
	src := `
resource "aws_vpc" "main" {
  cidr_block = "10.0.0.0/16"
}

resource MISSING_BRACES

resource "aws_subnet" "pub" {
  vpc_id = aws_vpc.main.id
}
`
	file, errs := parseWithErrors(t, src)
	if len(errs) == 0 {
		t.Fatal("expected parse errors, got none")
	}
	// Both well-formed resources should be in the AST.
	var blocks []string
	for _, n := range file.Body.Nodes {
		if b, ok := n.(*ast.Block); ok {
			if len(b.Labels) == 2 {
				blocks = append(blocks, b.Labels[1])
			}
		}
	}
	found := map[string]bool{}
	for _, name := range blocks {
		found[name] = true
	}
	if !found["main"] {
		t.Error("block 'main' before bad block should have been parsed")
	}
	if !found["pub"] {
		t.Error("block 'pub' after bad block should have been parsed")
	}
}

func TestRecoveryMissingOpenBrace(t *testing.T) {
	src := `
variable "good_before" { type = string }
resource "aws_vpc" "main"
  cidr_block = "10.0.0.0/16"
variable "good_after" { type = string }
`
	file, errs := parseWithErrors(t, src)
	if len(errs) == 0 {
		t.Fatal("expected at least one parse error")
	}
	names := map[string]bool{}
	for _, n := range file.Body.Nodes {
		if b, ok := n.(*ast.Block); ok && len(b.Labels) == 1 {
			names[b.Labels[0]] = true
		}
	}
	if !names["good_before"] {
		t.Error("variable 'good_before' should have been parsed")
	}
	if !names["good_after"] {
		t.Error("variable 'good_after' should have been parsed")
	}
}

func TestRecoveryBadExpressionInAttribute(t *testing.T) {
	src := "before = 1\nbad_attr = ???\nafter = 3\n"
	file, errs := parseWithErrors(t, src)
	if len(errs) == 0 {
		t.Fatal("expected parse errors, got none")
	}
	names := map[string]bool{}
	for _, n := range file.Body.Nodes {
		if a, ok := n.(*ast.Attribute); ok {
			names[a.Name] = true
		}
	}
	if !names["before"] {
		t.Error("attribute 'before' should have been parsed")
	}
	if !names["after"] {
		t.Error("attribute 'after' should have been parsed")
	}
}

func TestRecoveryNestedBadAttributeDoesNotEscapeBlock(t *testing.T) {
	// An error inside a block body should not affect parsing outside the block.
	src := `
resource "aws_vpc" "bad" {
  ??? = invalid
}
resource "aws_vpc" "good" {
  cidr_block = "10.0.0.0/16"
}
`
	file, errs := parseWithErrors(t, src)
	if len(errs) == 0 {
		t.Fatal("expected parse errors, got none")
	}
	var names []string
	for _, n := range file.Body.Nodes {
		if b, ok := n.(*ast.Block); ok && len(b.Labels) == 2 {
			names = append(names, b.Labels[1])
		}
	}
	found := map[string]bool{}
	for _, n := range names {
		found[n] = true
	}
	if !found["bad"] {
		t.Error("block 'bad' should appear in AST even with errors")
	}
	if !found["good"] {
		t.Error("block 'good' after error block should have been parsed")
	}
}

func TestRecoverySingleErrorPerMistake(t *testing.T) {
	// One bad line should produce a small number of errors, not a cascade.
	src := "good = 1\n??? bad tokens here!!!\nafter = 2\n"
	_, errs := parseWithErrors(t, src)
	// Generous upper bound: a single bad line should not produce more than 3 errors.
	if len(errs) > 3 {
		t.Errorf("expected ≤3 errors for one bad line, got %d: %v", len(errs), errs)
	}
}

func TestWalkCollectsAttributes(t *testing.T) {
	src := `
resource "aws_instance" "web" {
  ami           = "ami-123"
  instance_type = "t2.micro"
}
`
	file := mustParse(t, src)

	var names []string
	ast.Inspect(file, func(n ast.Node) bool {
		if attr, ok := n.(*ast.Attribute); ok {
			names = append(names, attr.Name)
		}
		return true
	})

	want := map[string]bool{"ami": true, "instance_type": true}
	for _, name := range names {
		delete(want, name)
	}
	if len(want) > 0 {
		t.Errorf("missing attributes: %v", want)
	}
}
