package printer

import (
	"bytes"
	"fmt"
	"strings"
	"github.com/dgr237/tflens/pkg/ast"
)

// Print formats a parsed file as normalised HCL text.
// Comments are not preserved (they are discarded by the lexer).
// The output is not byte-for-byte identical to the source, but it is
// semantically equivalent and idempotent: Print(Parse(Print(Parse(src)))) == Print(Parse(src)).
func Print(file *ast.File) string {
	p := &printer{}
	p.printBody(file.Body)
	return p.buf.String()
}

// PrintExpr formats a single expression in its canonical form. Two expressions
// that differ only in whitespace or trivial formatting will produce identical
// strings.
func PrintExpr(e ast.Expr) string {
	if e == nil {
		return ""
	}
	p := &printer{}
	p.printExpr(e, 0)
	return p.buf.String()
}

type printer struct {
	buf   bytes.Buffer
	depth int
}

func (p *printer) write(s string)            { p.buf.WriteString(s) }
func (p *printer) writef(f string, a ...any) { fmt.Fprintf(&p.buf, f, a...) }
func (p *printer) writeByte(b byte)          { p.buf.WriteByte(b) }

func (p *printer) indent() {
	for i := 0; i < p.depth; i++ {
		p.buf.WriteByte('\t')
	}
}

// ---- structural nodes ----

func (p *printer) printBody(body *ast.Body) {
	for i, node := range body.Nodes {
		switch n := node.(type) {
		case *ast.Block:
			// Blank line before every block that is not the first item,
			// unless the block has leading comments (the blank line goes
			// before the comment block instead, preserving visual grouping).
			if i > 0 {
				p.writeByte('\n')
			}
			p.printLeadingComments(n.LeadingComments)
			p.printBlock(n)
		case *ast.Attribute:
			p.printLeadingComments(n.LeadingComments)
			p.indent()
			p.printAttribute(n)
		}
	}
	// Body-trailing comments (after the last node, before the closer).
	for _, c := range body.TrailingComments {
		p.indent()
		p.write(c)
		p.writeByte('\n')
	}
}

// printLeadingComments emits each comment on its own line, indented at the
// current depth. Does nothing when comments is empty.
func (p *printer) printLeadingComments(comments []string) {
	for _, c := range comments {
		p.indent()
		p.write(c)
		p.writeByte('\n')
	}
}

func (p *printer) printBlock(b *ast.Block) {
	p.indent()
	p.write(b.Type)
	for _, label := range b.Labels {
		p.writeByte(' ')
		p.writeQuotedString(label)
	}
	p.write(" {")
	if b.TrailingComment != "" {
		p.writeByte(' ')
		p.write(b.TrailingComment)
	}
	p.writeByte('\n')
	p.depth++
	p.printBody(b.Body)
	p.depth--
	p.indent()
	p.write("}\n")
}

func (p *printer) printAttribute(a *ast.Attribute) {
	p.write(a.Name)
	p.write(" = ")
	p.printExpr(a.Value, 0)
	if a.TrailingComment != "" {
		p.writeByte(' ')
		p.write(a.TrailingComment)
	}
	p.writeByte('\n')
}

// ---- expressions ----

// precAtom is a sentinel meaning "the sub-expression must be atomic — wrap
// anything compound in parentheses". Used for the operand of unary ops and
// for the collection in index / splat expressions.
const precAtom = 99

// binaryPrec returns the precedence of a binary operator (higher = tighter).
func binaryPrec(op string) int {
	switch op {
	case "||":
		return 1
	case "&&":
		return 2
	case "==", "!=":
		return 3
	case "<", ">", "<=", ">=":
		return 4
	case "+", "-":
		return 5
	case "*", "/", "%":
		return 6
	}
	return 0
}

// printExpr prints expr, wrapping it in parentheses if its own precedence
// is less than minPrec.
func (p *printer) printExpr(e ast.Expr, minPrec int) {
	switch n := e.(type) {

	case *ast.LiteralExpr:
		p.printLiteral(n)

	case *ast.RefExpr:
		p.write(strings.Join(n.Parts, "."))

	case *ast.TemplateExpr:
		p.writeByte('"')
		for _, part := range n.Parts {
			if part.IsLiteral {
				p.write(escapeStringContent(part.Literal))
			} else {
				p.write("${")
				p.printExpr(part.Expr, 0)
				p.writeByte('}')
			}
		}
		p.writeByte('"')

	case *ast.IndexExpr:
		p.printExpr(n.Collection, precAtom)
		p.writeByte('[')
		p.printExpr(n.Key, 0)
		p.writeByte(']')

	case *ast.SplatExpr:
		p.printExpr(n.Source, precAtom)
		p.write("[*]")
		// Each is the traversal continuation after [*]; print as .part chains.
		if ref, ok := n.Each.(*ast.RefExpr); ok {
			for _, part := range ref.Parts {
				p.writeByte('.')
				p.write(part)
			}
		} else {
			p.printExpr(n.Each, precAtom)
		}

	case *ast.CallExpr:
		p.write(n.Name)
		p.writeByte('(')
		for i, arg := range n.Args {
			if i > 0 {
				p.write(", ")
			}
			p.printExpr(arg, 0)
			if i == len(n.Args)-1 && n.ExpandLast {
				p.write("...")
			}
		}
		p.writeByte(')')

	case *ast.TupleExpr:
		p.writeByte('[')
		for i, item := range n.Items {
			if i > 0 {
				p.write(", ")
			}
			p.printExpr(item, 0)
		}
		p.writeByte(']')

	case *ast.ObjectExpr:
		p.printObject(n)

	case *ast.ForExpr:
		p.printForExpr(n)

	case *ast.CondExpr:
		// Ternary has the lowest precedence of all; wrap if inside a binary op.
		needParens := minPrec > 0
		if needParens {
			p.writeByte('(')
		}
		// Pred must not itself be a bare ternary (would be ambiguous).
		p.printExpr(n.Pred, 1)
		p.write(" ? ")
		p.printExpr(n.True, 0)
		p.write(" : ")
		p.printExpr(n.False, 0)
		if needParens {
			p.writeByte(')')
		}

	case *ast.BinaryExpr:
		prec := binaryPrec(n.Op)
		if prec < minPrec {
			p.writeByte('(')
		}
		p.printExpr(n.Left, prec)
		p.writef(" %s ", n.Op)
		// Right side uses prec+1 to force parentheses on equal-precedence
		// right operands, correctly reflecting left-associativity.
		p.printExpr(n.Right, prec+1)
		if prec < minPrec {
			p.writeByte(')')
		}

	case *ast.UnaryExpr:
		p.write(n.Op)
		p.printExpr(n.Operand, precAtom)
	}
}

func (p *printer) printLiteral(n *ast.LiteralExpr) {
	switch v := n.Value.(type) {
	case nil:
		p.write("null")
	case bool:
		if v {
			p.write("true")
		} else {
			p.write("false")
		}
	case float64:
		if v == float64(int64(v)) {
			p.writef("%d", int64(v))
		} else {
			p.writef("%g", v)
		}
	case string:
		// Heredoc literals carry their own markers — emit verbatim.
		if strings.HasPrefix(v, "<<") {
			p.write(v)
		} else {
			p.writeByte('"')
			p.write(escapeStringContent(v))
			p.writeByte('"')
		}
	}
}

func (p *printer) printObject(n *ast.ObjectExpr) {
	if len(n.Items) == 0 {
		p.write("{}")
		return
	}
	p.write("{\n")
	p.depth++
	for _, item := range n.Items {
		p.indent()
		p.printExpr(item.Key, 0)
		p.write(" = ")
		p.printExpr(item.Value, 0)
		p.writeByte('\n')
	}
	p.depth--
	p.indent()
	p.writeByte('}')
}

func (p *printer) printForExpr(n *ast.ForExpr) {
	isObject := n.KeyExpr != nil
	if isObject {
		p.writeByte('{')
	} else {
		p.writeByte('[')
	}
	p.write("for ")
	if n.KeyVar != "" {
		p.write(n.KeyVar)
		p.write(", ")
	}
	p.write(n.ValVar)
	p.write(" in ")
	p.printExpr(n.CollExpr, 0)
	p.write(" : ")
	if isObject {
		p.printExpr(n.KeyExpr, 0)
		p.write(" => ")
	}
	p.printExpr(n.ValExpr, 0)
	if n.CondExpr != nil {
		p.write(" if ")
		p.printExpr(n.CondExpr, 0)
	}
	if isObject {
		p.writeByte('}')
	} else {
		p.writeByte(']')
	}
}

// ---- string helpers ----

func (p *printer) writeQuotedString(s string) {
	p.writeByte('"')
	p.write(escapeStringContent(s))
	p.writeByte('"')
}

// escapeStringContent escapes special characters inside a HCL string literal.
func escapeStringContent(s string) string {
	s = strings.ReplaceAll(s, `\`, `\\`)
	s = strings.ReplaceAll(s, `"`, `\"`)
	s = strings.ReplaceAll(s, "\n", `\n`)
	s = strings.ReplaceAll(s, "\r", `\r`)
	s = strings.ReplaceAll(s, "\t", `\t`)
	return s
}
