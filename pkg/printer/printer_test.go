package printer_test

import (
	"os"
	"strings"
	"testing"
	"github.com/dgr237/tflens/pkg/parser"
	"github.com/dgr237/tflens/pkg/printer"
)

// roundTrip parses src, prints it, parses again, prints again, and returns
// both printed outputs. A correct printer is idempotent: out1 == out2.
func roundTrip(t *testing.T, src string) (out1, out2 string) {
	t.Helper()

	file1, errs := parser.ParseFile([]byte(src), "test.tf")
	if len(errs) > 0 {
		for _, e := range errs {
			t.Errorf("parse1 error: %s", e)
		}
		t.FailNow()
	}
	out1 = printer.Print(file1)

	file2, errs := parser.ParseFile([]byte(out1), "test.tf")
	if len(errs) > 0 {
		for _, e := range errs {
			t.Errorf("parse2 error: %s\nprinted output:\n%s", e, out1)
		}
		t.FailNow()
	}
	out2 = printer.Print(file2)
	return out1, out2
}

func assertRoundTrip(t *testing.T, src string) string {
	t.Helper()
	out1, out2 := roundTrip(t, src)
	if out1 != out2 {
		t.Errorf("printer is not idempotent\n--- first pass ---\n%s\n--- second pass ---\n%s", out1, out2)
	}
	return out1
}

// ---- unit tests for specific constructs ----

func TestPrintAttribute(t *testing.T) {
	out := assertRoundTrip(t, `name = "world"`)
	if !strings.Contains(out, `name = "world"`) {
		t.Errorf("unexpected output: %q", out)
	}
}

func TestPrintBlock(t *testing.T) {
	src := `
resource "aws_instance" "web" {
  ami = "ami-123"
}
`
	out := assertRoundTrip(t, src)
	if !strings.Contains(out, `resource "aws_instance" "web"`) {
		t.Errorf("block header missing in: %s", out)
	}
	if !strings.Contains(out, `ami = "ami-123"`) {
		t.Errorf("attribute missing in: %s", out)
	}
}

func TestPrintBoolAndNull(t *testing.T) {
	out := assertRoundTrip(t, "a = true\nb = false\nc = null")
	if !strings.Contains(out, "a = true") {
		t.Errorf("true missing in: %s", out)
	}
	if !strings.Contains(out, "b = false") {
		t.Errorf("false missing in: %s", out)
	}
	if !strings.Contains(out, "c = null") {
		t.Errorf("null missing in: %s", out)
	}
}

func TestPrintNumber(t *testing.T) {
	out := assertRoundTrip(t, "a = 42\nb = 3.14")
	if !strings.Contains(out, "a = 42") {
		t.Errorf("integer missing in: %s", out)
	}
	if !strings.Contains(out, "b = 3.14") {
		t.Errorf("float missing in: %s", out)
	}
}

func TestPrintReference(t *testing.T) {
	out := assertRoundTrip(t, `x = aws_instance.web.id`)
	if !strings.Contains(out, "aws_instance.web.id") {
		t.Errorf("reference missing in: %s", out)
	}
}

func TestPrintFunctionCall(t *testing.T) {
	out := assertRoundTrip(t, `x = tostring(42)`)
	if !strings.Contains(out, "tostring(42)") {
		t.Errorf("call missing in: %s", out)
	}
}

func TestPrintTemplateString(t *testing.T) {
	out := assertRoundTrip(t, `x = "hello ${var.name}"`)
	if !strings.Contains(out, `"hello ${var.name}"`) {
		t.Errorf("template missing in: %s", out)
	}
}

func TestPrintTuple(t *testing.T) {
	out := assertRoundTrip(t, `cidrs = ["10.0.0.0/8", "172.16.0.0/12"]`)
	if !strings.Contains(out, `"10.0.0.0/8"`) {
		t.Errorf("tuple missing in: %s", out)
	}
}

func TestPrintObject(t *testing.T) {
	out := assertRoundTrip(t, `tags = { Name = "web", Env = "prod" }`)
	if !strings.Contains(out, "Name") || !strings.Contains(out, `"web"`) {
		t.Errorf("object missing in: %s", out)
	}
}

func TestPrintBinaryExpr(t *testing.T) {
	// Precedence must be preserved: 1 + 2 * 3 should NOT become (1 + 2) * 3
	out := assertRoundTrip(t, `x = 1 + 2 * 3`)
	if strings.Contains(out, "(1 + 2)") {
		t.Errorf("printer added unnecessary parens: %s", out)
	}
}

func TestPrintPrecedenceParens(t *testing.T) {
	// (1 + 2) * 3 must keep parens because + has lower prec than *
	out := assertRoundTrip(t, `x = (1 + 2) * 3`)
	if !strings.Contains(out, "(1 + 2)") {
		t.Errorf("printer dropped necessary parens: %s", out)
	}
}

func TestPrintUnary(t *testing.T) {
	out := assertRoundTrip(t, `x = !enabled`)
	if !strings.Contains(out, "!enabled") {
		t.Errorf("unary missing in: %s", out)
	}
}

func TestPrintConditional(t *testing.T) {
	out := assertRoundTrip(t, `x = count > 0 ? "yes" : "no"`)
	if !strings.Contains(out, `? "yes" : "no"`) {
		t.Errorf("conditional missing in: %s", out)
	}
}

func TestPrintForListExpr(t *testing.T) {
	out := assertRoundTrip(t, `names = [for s in var.list : s.name]`)
	if !strings.Contains(out, "for s in") {
		t.Errorf("for expr missing in: %s", out)
	}
}

func TestPrintForObjectExpr(t *testing.T) {
	out := assertRoundTrip(t, `m = {for k, v in var.map : k => v}`)
	if !strings.Contains(out, "for k, v in") || !strings.Contains(out, "=>") {
		t.Errorf("for object expr missing in: %s", out)
	}
}

func TestPrintForExprWithIf(t *testing.T) {
	out := assertRoundTrip(t, `x = [for s in var.list : s if s != ""]`)
	if !strings.Contains(out, " if ") {
		t.Errorf("for if clause missing in: %s", out)
	}
}

// ---- comment preservation ----

func TestLeadingCommentOnAttribute(t *testing.T) {
	src := "# a leading note\nname = \"hello\"\n"
	out := assertRoundTrip(t, src)
	if !strings.Contains(out, "# a leading note") {
		t.Errorf("leading comment not preserved: %q", out)
	}
}

func TestTrailingCommentOnAttribute(t *testing.T) {
	src := "name = \"hello\" # trailing\n"
	out := assertRoundTrip(t, src)
	if !strings.Contains(out, "# trailing") {
		t.Errorf("trailing comment not preserved: %q", out)
	}
	if !strings.Contains(out, "name = \"hello\" # trailing") {
		t.Errorf("trailing comment not on same line: %q", out)
	}
}

func TestLeadingCommentOnBlock(t *testing.T) {
	src := "# describes the VPC\nresource \"aws_vpc\" \"main\" {\n}\n"
	out := assertRoundTrip(t, src)
	if !strings.Contains(out, "# describes the VPC") {
		t.Errorf("leading comment on block not preserved: %q", out)
	}
}

func TestCommentBetweenBlocksGoesToNextBlock(t *testing.T) {
	// The standalone comment between two blocks belongs to the following
	// block (convention: blank-line-separated comments lead the next statement).
	src := "resource \"aws_vpc\" \"a\" {}\n\n# header for b\nresource \"aws_vpc\" \"b\" {}\n"
	out := assertRoundTrip(t, src)
	// Find the comment and ensure it precedes the b block, not the a block.
	idxComment := strings.Index(out, "# header for b")
	idxA := strings.Index(out, "\"a\"")
	idxB := strings.Index(out, "\"b\"")
	if idxComment < 0 || idxA < 0 || idxB < 0 {
		t.Fatalf("expected all markers in output, got: %q", out)
	}
	if !(idxA < idxComment && idxComment < idxB) {
		t.Errorf("comment should be between a and b:\n%s", out)
	}
}

func TestBlockCommentPreserved(t *testing.T) {
	src := "/* a block comment */\nx = 1\n"
	out := assertRoundTrip(t, src)
	if !strings.Contains(out, "/* a block comment */") {
		t.Errorf("block comment not preserved: %q", out)
	}
}

func TestMultipleLeadingComments(t *testing.T) {
	src := "# line 1\n# line 2\nx = 1\n"
	out := assertRoundTrip(t, src)
	if !strings.Contains(out, "# line 1") || !strings.Contains(out, "# line 2") {
		t.Errorf("multiple leading comments not preserved: %q", out)
	}
}

func TestCommentsInsideBlockBody(t *testing.T) {
	src := "resource \"x\" \"y\" {\n  # leading on attr\n  name = \"v\" # trailing\n}\n"
	out := assertRoundTrip(t, src)
	if !strings.Contains(out, "# leading on attr") {
		t.Errorf("leading-inside-block not preserved: %q", out)
	}
	if !strings.Contains(out, "# trailing") {
		t.Errorf("trailing-inside-block not preserved: %q", out)
	}
}

// ---- round-trip against the real smoke file ----

func TestRoundTripSmokeFile(t *testing.T) {
	src, err := os.ReadFile("../../testdata/smoke.tf")
	if err != nil {
		t.Fatalf("could not read smoke.tf: %v", err)
	}
	out1, out2 := roundTrip(t, string(src))
	if out1 != out2 {
		// Find first differing line for a useful error message.
		lines1 := strings.Split(out1, "\n")
		lines2 := strings.Split(out2, "\n")
		for i := 0; i < len(lines1) && i < len(lines2); i++ {
			if lines1[i] != lines2[i] {
				t.Errorf("first diff at line %d:\n  pass1: %q\n  pass2: %q", i+1, lines1[i], lines2[i])
				break
			}
		}
		if len(lines1) != len(lines2) {
			t.Errorf("line count differs: pass1=%d pass2=%d", len(lines1), len(lines2))
		}
		t.Logf("--- pass 1 output ---\n%s", out1)
	}
}
