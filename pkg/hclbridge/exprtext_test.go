package hclbridge_test

import (
	"testing"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/dgr237/tflens/pkg/ast"
	"github.com/dgr237/tflens/pkg/hclbridge"
	"github.com/dgr237/tflens/pkg/parser"
	"github.com/dgr237/tflens/pkg/printer"
)

// The diff layer's contract: given two expressions, produce the same
// "equal vs different" verdict from canonical-text comparison. The bridge
// must uphold the same contract using hclwrite.Format in place of
// printer.PrintExpr.
func TestExprTextDiffVerdictMatchesPrintExpr(t *testing.T) {
	cases := []struct {
		name            string
		oldSrc, newSrc  string
		wantEqualVerdict bool // true if both paths should call these "equal"
	}{
		{
			name:            "identical literal",
			oldSrc:          `locals { x = "hello" }`,
			newSrc:          `locals { x = "hello" }`,
			wantEqualVerdict: true,
		},
		{
			name:            "whitespace-only change",
			oldSrc:          "locals { x = merge(var.a, { k = 1 }) }",
			newSrc:          "locals { x = merge( var.a, { k = 1 } ) }",
			wantEqualVerdict: true,
		},
		{
			name:            "literal value changed",
			oldSrc:          `locals { x = "hello" }`,
			newSrc:          `locals { x = "world" }`,
			wantEqualVerdict: false,
		},
		{
			name:            "ref changed",
			oldSrc:          `locals { x = var.a }`,
			newSrc:          `locals { x = var.b }`,
			wantEqualVerdict: false,
		},
		{
			name:            "function call args reordered",
			oldSrc:          `locals { x = merge(var.a, var.b) }`,
			newSrc:          `locals { x = merge(var.b, var.a) }`,
			wantEqualVerdict: false,
		},
		{
			name:            "template changed",
			oldSrc:          `locals { x = "${var.a}-foo" }`,
			newSrc:          `locals { x = "${var.a}-bar" }`,
			wantEqualVerdict: false,
		},
		{
			name:            "conditional branches swapped",
			oldSrc:          `locals { x = var.cond ? "a" : "b" }`,
			newSrc:          `locals { x = var.cond ? "b" : "a" }`,
			wantEqualVerdict: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Old path: parse with internal parser, PrintExpr the local's value.
			oldText := printLocalExpr(t, tc.oldSrc, "oldfile.tf")
			newText := printLocalExpr(t, tc.newSrc, "newfile.tf")
			oldVerdict := oldText == newText

			// Bridge path: ExprText the local's value.
			oldBridge := bridgeLocalExpr(t, tc.oldSrc, "oldfile.tf")
			newBridge := bridgeLocalExpr(t, tc.newSrc, "newfile.tf")
			newVerdict := oldBridge == newBridge

			if oldVerdict != tc.wantEqualVerdict {
				t.Errorf("old path verdict wrong: oldText=%q newText=%q want=%v got=%v",
					oldText, newText, tc.wantEqualVerdict, oldVerdict)
			}
			if newVerdict != tc.wantEqualVerdict {
				t.Errorf("bridge verdict wrong: oldBridge=%q newBridge=%q want=%v got=%v",
					oldBridge, newBridge, tc.wantEqualVerdict, newVerdict)
			}
		})
	}
}

func printLocalExpr(t *testing.T, src, filename string) string {
	t.Helper()
	file, errs := parser.ParseFile([]byte(src), filename)
	if len(errs) > 0 {
		t.Fatalf("internal parse %s: %v", filename, errs)
	}
	for _, node := range file.Body.Nodes {
		block, ok := node.(*ast.Block)
		if !ok || block.Type != "locals" {
			continue
		}
		for _, n := range block.Body.Nodes {
			if attr, ok := n.(*ast.Attribute); ok && attr.Name == "x" {
				return printer.PrintExpr(attr.Value)
			}
		}
	}
	t.Fatalf("locals.x not found in %q", src)
	return ""
}

func bridgeLocalExpr(t *testing.T, src, filename string) string {
	t.Helper()
	p := hclparse.NewParser()
	file, diags := p.ParseHCL([]byte(src), filename)
	if diags.HasErrors() {
		t.Fatalf("hcl parse %s: %s", filename, diags.Error())
	}
	body := file.Body.(*hclsyntax.Body)
	for _, block := range body.Blocks {
		if block.Type != "locals" {
			continue
		}
		if attr, ok := block.Body.Attributes["x"]; ok {
			return hclbridge.ExprText(attr.Expr, []byte(src))
		}
	}
	t.Fatalf("locals.x not found in %q", src)
	return ""
}
