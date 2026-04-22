package lexer_test

import (
	"testing"
	"github.com/dgr237/tflens/pkg/lexer"
	"github.com/dgr237/tflens/pkg/token"
)

type tokenSpec struct {
	typ     token.TokenType
	literal string
}

func tokenize(src string) []tokenSpec {
	l := lexer.New([]byte(src), "test")
	tokens := l.Tokens()
	out := make([]tokenSpec, len(tokens))
	for i, t := range tokens {
		out[i] = tokenSpec{t.Type, t.Literal}
	}
	return out
}

func assertTokens(t *testing.T, src string, want []tokenSpec) {
	t.Helper()
	got := tokenize(src)
	// strip trailing EOF for brevity unless the want list includes it
	if len(want) > 0 && want[len(want)-1].typ != token.EOF {
		if len(got) > 0 && got[len(got)-1].typ == token.EOF {
			got = got[:len(got)-1]
		}
	}
	if len(got) != len(want) {
		t.Fatalf("token count mismatch: got %d, want %d\ngot:  %v\nwant: %v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("token[%d]: got %v, want %v", i, got[i], want[i])
		}
	}
}

func TestSimpleAttribute(t *testing.T) {
	assertTokens(t, `name = "world"`, []tokenSpec{
		{token.IDENT, "name"},
		{token.EQUALS, "="},
		{token.STRING, "world"},
	})
}

func TestBoolAndNull(t *testing.T) {
	assertTokens(t, `enabled = true`, []tokenSpec{
		{token.IDENT, "enabled"},
		{token.EQUALS, "="},
		{token.BOOL, "true"},
	})
	assertTokens(t, `v = null`, []tokenSpec{
		{token.IDENT, "v"},
		{token.EQUALS, "="},
		{token.NULL, "null"},
	})
}

func TestNumber(t *testing.T) {
	assertTokens(t, `count = 3`, []tokenSpec{
		{token.IDENT, "count"},
		{token.EQUALS, "="},
		{token.NUMBER, "3"},
	})
	assertTokens(t, `ratio = 1.5`, []tokenSpec{
		{token.IDENT, "ratio"},
		{token.EQUALS, "="},
		{token.NUMBER, "1.5"},
	})
}

func TestBlock(t *testing.T) {
	src := "resource \"aws_instance\" \"web\" {\n}"
	assertTokens(t, src, []tokenSpec{
		{token.IDENT, "resource"},
		{token.STRING, "aws_instance"},
		{token.STRING, "web"},
		{token.LBRACE, "{"},
		{token.RBRACE, "}"},
	})
}

func TestTemplateString(t *testing.T) {
	assertTokens(t, `name = "hello ${var.name}"`, []tokenSpec{
		{token.IDENT, "name"},
		{token.EQUALS, "="},
		{token.STRING, "hello "},
		{token.TEMPLATE_START, "${"},
		{token.IDENT, "var"},
		{token.DOT, "."},
		{token.IDENT, "name"},
		{token.RBRACE, "}"},
		{token.STRING, ""},
	})
}

func TestLineComment(t *testing.T) {
	// Comments are emitted as COMMENT tokens so the parser can attach them to
	// nearby statements. The \n that terminates the comment line is still
	// emitted as NEWLINE.
	assertTokens(t, "# comment\nfoo = 1", []tokenSpec{
		{token.COMMENT, "# comment"},
		{token.NEWLINE, "\n"},
		{token.IDENT, "foo"},
		{token.EQUALS, "="},
		{token.NUMBER, "1"},
	})
}

func TestSlashSlashComment(t *testing.T) {
	assertTokens(t, "// hi\nfoo = 1", []tokenSpec{
		{token.COMMENT, "// hi"},
		{token.NEWLINE, "\n"},
		{token.IDENT, "foo"},
		{token.EQUALS, "="},
		{token.NUMBER, "1"},
	})
}

func TestBlockComment(t *testing.T) {
	assertTokens(t, "/* hello */ foo = 1", []tokenSpec{
		{token.COMMENT, "/* hello */"},
		{token.IDENT, "foo"},
		{token.EQUALS, "="},
		{token.NUMBER, "1"},
	})
}

func TestNewlineSuppressedInsideBrackets(t *testing.T) {
	src := "list = [\n  1,\n  2,\n]"
	got := tokenize(src)
	for _, tok := range got {
		if tok.typ == token.NEWLINE {
			t.Errorf("unexpected NEWLINE inside brackets: %v", tok)
		}
	}
}

func TestFatArrow(t *testing.T) {
	assertTokens(t, `k => v`, []tokenSpec{
		{token.IDENT, "k"},
		{token.FAT_ARROW, "=>"},
		{token.IDENT, "v"},
	})
}

func TestEllipsis(t *testing.T) {
	assertTokens(t, `func(args...)`, []tokenSpec{
		{token.IDENT, "func"},
		{token.LPAREN, "("},
		{token.IDENT, "args"},
		{token.ELLIPSIS, "..."},
		{token.RPAREN, ")"},
	})
}

func TestArithmeticOperators(t *testing.T) {
	assertTokens(t, `a + b - c * d / e % f`, []tokenSpec{
		{token.IDENT, "a"},
		{token.PLUS, "+"},
		{token.IDENT, "b"},
		{token.MINUS, "-"},
		{token.IDENT, "c"},
		{token.STAR, "*"},
		{token.IDENT, "d"},
		{token.SLASH, "/"},
		{token.IDENT, "e"},
		{token.PERCENT, "%"},
		{token.IDENT, "f"},
	})
}

func TestComparisonOperators(t *testing.T) {
	assertTokens(t, `a == b != c < d > e <= f >= g`, []tokenSpec{
		{token.IDENT, "a"},
		{token.EQ_EQ, "=="},
		{token.IDENT, "b"},
		{token.BANG_EQ, "!="},
		{token.IDENT, "c"},
		{token.LT, "<"},
		{token.IDENT, "d"},
		{token.GT, ">"},
		{token.IDENT, "e"},
		{token.LT_EQ, "<="},
		{token.IDENT, "f"},
		{token.GT_EQ, ">="},
		{token.IDENT, "g"},
	})
}

func TestLogicalOperators(t *testing.T) {
	assertTokens(t, `a && b || !c`, []tokenSpec{
		{token.IDENT, "a"},
		{token.AND_AND, "&&"},
		{token.IDENT, "b"},
		{token.OR_OR, "||"},
		{token.BANG, "!"},
		{token.IDENT, "c"},
	})
}

func TestUnaryMinus(t *testing.T) {
	// Negative numbers are now unary minus + number, not a single token.
	assertTokens(t, `-1`, []tokenSpec{
		{token.MINUS, "-"},
		{token.NUMBER, "1"},
	})
}
