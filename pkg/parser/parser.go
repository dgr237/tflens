package parser

import (
	"fmt"
	"strconv"
	"github.com/dgr237/tflens/pkg/ast"
	"github.com/dgr237/tflens/pkg/lexer"
	"github.com/dgr237/tflens/pkg/token"
)

// ParseError describes a single parse failure.
type ParseError struct {
	Pos token.Position
	Msg string
}

func (e ParseError) Error() string {
	return fmt.Sprintf("%s: %s", e.Pos, e.Msg)
}

// Parser converts a token stream into an AST.
type Parser struct {
	lexer           *lexer.Lexer
	current         token.Token
	peek            token.Token
	errors          []ParseError
	pendingComments []string // comments absorbed since caller last drained
}

// New creates a Parser and primes the two-token lookahead.
func New(l *lexer.Lexer) *Parser {
	p := &Parser{lexer: l}
	// Prime peek with the first real token (skipping any leading comments
	// into pendingComments), then advance once more so current holds the
	// first real token and peek holds the second.
	p.peek = p.readRealToken()
	p.advance()
	return p
}

// ParseFile parses a complete Terraform source file.
func ParseFile(src []byte, filename string) (*ast.File, []ParseError) {
	p := New(lexer.New(src, filename))
	pos := p.current.Pos
	body := p.parseBody(token.EOF)
	return &ast.File{Body: body, Pos: pos}, p.errors
}

// Errors returns any parse errors encountered so far.
func (p *Parser) Errors() []ParseError { return p.errors }

// ---- token stream helpers ----

func (p *Parser) advance() token.Token {
	prev := p.current
	p.current = p.peek
	p.peek = p.readRealToken()
	return prev
}

// readRealToken returns the next non-COMMENT token from the lexer,
// absorbing any COMMENT tokens into pendingComments. This keeps comments
// invisible to the normal parser state while preserving their text for
// later attachment to the nearest statement.
func (p *Parser) readRealToken() token.Token {
	for {
		t := p.lexer.Next()
		if t.Type != token.COMMENT {
			return t
		}
		p.pendingComments = append(p.pendingComments, t.Literal)
	}
}

// takeComments returns and clears the pending-comments buffer.
func (p *Parser) takeComments() []string {
	if len(p.pendingComments) == 0 {
		return nil
	}
	out := p.pendingComments
	p.pendingComments = nil
	return out
}

func (p *Parser) skip(t token.TokenType) {
	for p.current.Type == t {
		p.advance()
	}
}

func (p *Parser) skipNewlines() { p.skip(token.NEWLINE) }

func (p *Parser) expect(t token.TokenType) token.Token {
	tok := p.current
	if tok.Type != t {
		p.errorf("expected %s, got %s (%q)", t, tok.Type, tok.Literal)
	} else {
		p.advance()
	}
	return tok
}

func (p *Parser) errorf(format string, args ...any) {
	p.errors = append(p.errors, ParseError{
		Pos: p.current.Pos,
		Msg: fmt.Sprintf(format, args...),
	})
}

// syncStatement skips tokens until the next clean statement boundary:
// a NEWLINE (consumed), a RBRACE (left for the caller), or EOF.
// Call this immediately after recording an error to prevent cascading noise.
func (p *Parser) syncStatement() {
	for {
		switch p.current.Type {
		case token.EOF, token.RBRACE:
			return // leave these for the enclosing loop
		case token.NEWLINE:
			p.advance() // consume the newline; next token starts a fresh statement
			return
		}
		p.advance()
	}
}

// isAtStatementBoundary reports whether the current token is a position where
// a new statement could begin — used to decide whether recovery is needed.
func (p *Parser) isAtStatementBoundary() bool {
	switch p.current.Type {
	case token.NEWLINE, token.RBRACE, token.EOF:
		return true
	}
	return false
}

// ---- body / block / attribute ----

// parseBody reads nodes until it hits stopAt (typically RBRACE or EOF).
// Comments encountered before a statement become its LeadingComments;
// comments absorbed during a statement's parsing become its TrailingComment.
func (p *Parser) parseBody(stopAt token.TokenType) *ast.Body {
	pos := p.current.Pos
	body := &ast.Body{Pos: pos}

	for {
		p.skipNewlines()
		if p.current.Type == stopAt || p.current.Type == token.EOF {
			// Any comments between the last statement and the closer belong
			// to the body itself.
			body.TrailingComments = p.takeComments()
			break
		}

		leading := p.takeComments()
		node := p.parseNode()
		if node == nil {
			// parseNode returned nil: it saw a bad token at statement start and
			// advanced past one token. Clean up the rest of the line so we can
			// continue with the next statement. Drop the stranded comments.
			p.takeComments()
			p.syncStatement()
			continue
		}
		trailing := p.takeComments()
		attachLeadingComments(node, leading)
		attachTrailingComment(node, trailing)
		body.Nodes = append(body.Nodes, node)
	}
	return body
}

// attachLeadingComments writes leading comments onto a Block or Attribute.
func attachLeadingComments(n ast.Node, comments []string) {
	if len(comments) == 0 {
		return
	}
	switch node := n.(type) {
	case *ast.Block:
		node.LeadingComments = comments
	case *ast.Attribute:
		node.LeadingComments = comments
	}
}

// attachTrailingComment joins any trailing comments into a single string
// on the node. Multiple trailing comments for one statement are rare; when
// they occur we join with a space.
func attachTrailingComment(n ast.Node, comments []string) {
	if len(comments) == 0 {
		return
	}
	combined := comments[0]
	for _, c := range comments[1:] {
		combined += " " + c
	}
	switch node := n.(type) {
	case *ast.Block:
		node.TrailingComment = combined
	case *ast.Attribute:
		node.TrailingComment = combined
	}
}

// parseNode returns the next Block or Attribute, or nil on an unrecoverable token.
func (p *Parser) parseNode() ast.Node {
	if p.current.Type != token.IDENT {
		p.errorf("expected identifier, got %s (%q)", p.current.Type, p.current.Literal)
		// Advance past the bad token only if it is not a boundary — the body
		// loop's syncStatement will handle any remaining junk on the line.
		if !p.isAtStatementBoundary() {
			p.advance()
		}
		return nil
	}

	// Attribute: IDENT EQUALS expr
	if p.peek.Type == token.EQUALS {
		return p.parseAttribute()
	}

	// Block: IDENT label* LBRACE body RBRACE
	return p.parseBlock()
}

func (p *Parser) parseAttribute() *ast.Attribute {
	pos := p.current.Pos
	name := p.expect(token.IDENT).Literal
	p.expect(token.EQUALS)
	value := p.parseExpr()
	// NEWLINE consumption is handled by parseBody's loop top. Absorbing it
	// here would also consume any following standalone comment lines into
	// this attribute's trailing bucket, misplacing them.
	return &ast.Attribute{Name: name, Value: value, Pos: pos}
}

func (p *Parser) parseBlock() *ast.Block {
	pos := p.current.Pos
	blockType := p.expect(token.IDENT).Literal

	var labels []string
	for p.current.Type == token.STRING || p.current.Type == token.IDENT {
		labels = append(labels, p.current.Literal)
		p.advance()
	}

	if p.current.Type != token.LBRACE {
		// Missing opening brace — record the error and skip the rest of the
		// line.  Do not attempt to parse a body: we have no way to find the
		// matching closing brace and would consume unrelated tokens.
		p.errorf("expected '{' to open %q block, got %s (%q)", blockType, p.current.Type, p.current.Literal)
		p.syncStatement()
		return &ast.Block{Type: blockType, Labels: labels, Body: &ast.Body{Pos: pos}, Pos: pos}
	}

	p.advance() // consume {
	body := p.parseBody(token.RBRACE)
	p.expect(token.RBRACE)
	// See parseAttribute: leave the terminating NEWLINE for the enclosing
	// parseBody loop so that comments on the following line are not
	// misattributed as trailing on this block.

	return &ast.Block{Type: blockType, Labels: labels, Body: body, Pos: pos}
}

// ---- expressions — Pratt (precedence climbing) ----

// Precedence levels (higher = tighter binding).
const (
	precNone    = 0
	precOr      = 1 // ||
	precAnd     = 2 // &&
	precEqual   = 3 // == !=
	precCompare = 4 // < > <= >=
	precAdd     = 5 // + -
	precMul     = 6 // * / %
)

// infixPrec returns the precedence of t as an infix operator, or (0, false).
func infixPrec(t token.TokenType) (int, bool) {
	switch t {
	case token.OR_OR:
		return precOr, true
	case token.AND_AND:
		return precAnd, true
	case token.EQ_EQ, token.BANG_EQ:
		return precEqual, true
	case token.LT, token.GT, token.LT_EQ, token.GT_EQ:
		return precCompare, true
	case token.PLUS, token.MINUS:
		return precAdd, true
	case token.STAR, token.SLASH, token.PERCENT:
		return precMul, true
	}
	return 0, false
}

func (p *Parser) parseExpr() ast.Expr {
	expr := p.parseBinary(precNone)
	// Ternary is lower precedence than every binary operator, so it is checked
	// only after the full binary expression has been assembled.
	if p.current.Type == token.QUESTION {
		expr = p.parseConditional(expr)
	}
	return expr
}

// parseBinary implements precedence climbing.
func (p *Parser) parseBinary(minPrec int) ast.Expr {
	left := p.parseUnary()
	for {
		prec, ok := infixPrec(p.current.Type)
		if !ok || prec <= minPrec {
			break
		}
		op := p.advance()
		right := p.parseBinary(prec) // left-associative: pass prec (not prec-1)
		left = &ast.BinaryExpr{Op: op.Literal, Left: left, Right: right, Pos: op.Pos}
	}
	return left
}

// parseUnary handles prefix operators: ! and -
func (p *Parser) parseUnary() ast.Expr {
	if p.current.Type == token.BANG || p.current.Type == token.MINUS {
		op := p.advance()
		operand := p.parseUnary() // right-recursive for chained unary
		return &ast.UnaryExpr{Op: op.Literal, Operand: operand, Pos: op.Pos}
	}
	return p.parsePostfix()
}

// parsePostfix handles dot traversal and index after a primary.
func (p *Parser) parsePostfix() ast.Expr {
	expr := p.parsePrimary()
	for {
		switch p.current.Type {
		case token.DOT:
			expr = p.parseDotTraversal(expr)
		case token.LBRACKET:
			expr = p.parseIndex(expr)
		default:
			return expr
		}
	}
}

func (p *Parser) parsePrimary() ast.Expr {
	pos := p.current.Pos

	switch p.current.Type {
	case token.NULL:
		p.advance()
		return &ast.LiteralExpr{Value: nil, Pos: pos}

	case token.BOOL:
		lit := p.advance().Literal
		return &ast.LiteralExpr{Value: lit == "true", Pos: pos}

	case token.NUMBER:
		lit := p.advance().Literal
		v, err := strconv.ParseFloat(lit, 64)
		if err != nil {
			p.errorf("invalid number %q: %v", lit, err)
		}
		return &ast.LiteralExpr{Value: v, Pos: pos}

	case token.HEREDOC:
		lit := p.advance().Literal
		return &ast.LiteralExpr{Value: lit, Pos: pos}

	case token.STRING:
		return p.parseStringOrTemplate()

	case token.TEMPLATE_START:
		return p.parseStringOrTemplate()

	case token.LPAREN:
		p.advance() // consume (
		expr := p.parseExpr()
		p.expect(token.RPAREN)
		return expr

	case token.LBRACKET:
		return p.parseTuple()

	case token.LBRACE:
		return p.parseObject()

	case token.IDENT:
		// for expression: [for ...] or {for ...} are handled in parseTuple/parseObject
		// here we check for function call vs plain reference
		name := p.advance().Literal
		if p.current.Type == token.LPAREN {
			return p.parseCall(name, pos)
		}
		ref := &ast.RefExpr{Parts: []string{name}, Pos: pos}
		return ref

	}

	p.errorf("unexpected token %s (%q) in expression", p.current.Type, p.current.Literal)
	// Do not advance past boundary tokens — the body loop needs to see them.
	if !p.isAtStatementBoundary() {
		p.advance()
	}
	return &ast.LiteralExpr{Value: nil, Pos: pos}
}

// parseStringOrTemplate handles the sequence of STRING / TEMPLATE_START tokens
// that the lexer emits for a quoted string, producing either a plain LiteralExpr
// or a TemplateExpr when interpolation is present.
func (p *Parser) parseStringOrTemplate() ast.Expr {
	pos := p.current.Pos
	var parts []ast.TemplatePart

	for {
		switch p.current.Type {
		case token.STRING:
			lit := p.advance().Literal
			parts = append(parts, ast.TemplatePart{IsLiteral: true, Literal: lit})

		case token.TEMPLATE_START:
			p.advance() // consume ${
			expr := p.parseExpr()
			p.expect(token.RBRACE)
			parts = append(parts, ast.TemplatePart{IsLiteral: false, Expr: expr})

		default:
			// end of string
			goto done
		}
	}

done:
	// A string with no interpolation folds to a plain literal
	if len(parts) == 1 && parts[0].IsLiteral {
		return &ast.LiteralExpr{Value: parts[0].Literal, Pos: pos}
	}
	return &ast.TemplateExpr{Parts: parts, Pos: pos}
}

func (p *Parser) parseDotTraversal(left ast.Expr) ast.Expr {
	pos := p.current.Pos
	p.advance() // consume .

	// Splat: left.*
	if p.current.Type == token.STAR {
		p.advance()
		each := p.parsePrimary()
		return &ast.SplatExpr{Source: left, Each: each, Pos: pos}
	}

	if p.current.Type != token.IDENT {
		p.errorf("expected identifier after '.', got %s", p.current.Type)
		return left
	}

	part := p.advance().Literal

	// Append to existing RefExpr if possible to keep traversals flat
	if ref, ok := left.(*ast.RefExpr); ok {
		ref.Parts = append(ref.Parts, part)
		return ref
	}
	return &ast.IndexExpr{
		Collection: left,
		Key:        &ast.LiteralExpr{Value: part, Pos: p.current.Pos},
		Pos:        pos,
	}
}

func (p *Parser) parseIndex(left ast.Expr) ast.Expr {
	pos := p.current.Pos // position of [
	p.advance()          // consume [

	// Splat: left[*]
	if p.current.Type == token.STAR {
		p.advance()
		p.expect(token.RBRACKET)
		each := p.parseExpr()
		return &ast.SplatExpr{Source: left, Each: each, Pos: pos}
	}

	key := p.parseExpr()
	p.expect(token.RBRACKET)
	return &ast.IndexExpr{Collection: left, Key: key, Pos: pos}
}

func (p *Parser) parseCall(name string, pos token.Position) ast.Expr {
	p.advance() // consume (
	call := &ast.CallExpr{Name: name, Pos: pos}

	for p.current.Type != token.RPAREN && p.current.Type != token.EOF {
		arg := p.parseExpr()
		if p.current.Type == token.ELLIPSIS {
			p.advance()
			call.ExpandLast = true
			call.Args = append(call.Args, arg)
			break
		}
		call.Args = append(call.Args, arg)
		if p.current.Type == token.COMMA {
			p.advance()
		}
	}
	p.expect(token.RPAREN)
	return call
}

func (p *Parser) parseTuple() ast.Expr {
	pos := p.current.Pos
	p.advance() // consume [

	// for expression: [for ...]
	if p.current.Type == token.IDENT && p.current.Literal == "for" {
		return p.parseForExpr(pos, false)
	}

	tuple := &ast.TupleExpr{Pos: pos}
	for p.current.Type != token.RBRACKET && p.current.Type != token.EOF {
		p.skipNewlines()
		tuple.Items = append(tuple.Items, p.parseExpr())
		p.skipNewlines()
		if p.current.Type == token.COMMA {
			p.advance()
		}
	}
	p.expect(token.RBRACKET)
	return tuple
}

func (p *Parser) parseObject() ast.Expr {
	pos := p.current.Pos
	p.advance() // consume {

	// for expression: {for ...}
	if p.current.Type == token.IDENT && p.current.Literal == "for" {
		return p.parseForExpr(pos, true)
	}

	obj := &ast.ObjectExpr{Pos: pos}
	for p.current.Type != token.RBRACE && p.current.Type != token.EOF {
		p.skipNewlines()
		if p.current.Type == token.RBRACE {
			break
		}
		key := p.parseExpr()
		// Objects support both = and : as key/value separators
		if p.current.Type == token.COLON {
			p.advance()
		} else {
			p.expect(token.EQUALS)
		}
		val := p.parseExpr()
		// Items may be separated by a comma (inline) or a newline (block style)
		if p.current.Type == token.COMMA {
			p.advance()
		}
		p.skipNewlines()
		obj.Items = append(obj.Items, ast.ObjectItem{Key: key, Value: val})
	}
	p.expect(token.RBRACE)
	return obj
}

// parseForExpr parses a for expression after the opening [ or { has been consumed.
// isObject distinguishes {for ...} from [for ...].
func (p *Parser) parseForExpr(pos token.Position, isObject bool) ast.Expr {
	p.advance() // consume 'for'

	forExpr := &ast.ForExpr{Pos: pos}

	// for k, v in ... or for v in ...
	first := p.expect(token.IDENT).Literal
	if p.current.Type == token.COMMA {
		p.advance()
		forExpr.KeyVar = first
		forExpr.ValVar = p.expect(token.IDENT).Literal
	} else {
		forExpr.ValVar = first
	}

	if p.current.Type != token.IDENT || p.current.Literal != "in" {
		p.errorf("expected 'in' in for expression, got %q", p.current.Literal)
	} else {
		p.advance()
	}
	forExpr.CollExpr = p.parseExpr()

	p.expect(token.COLON)

	if isObject {
		forExpr.KeyExpr = p.parseExpr()
		p.expect(token.FAT_ARROW)
	}
	forExpr.ValExpr = p.parseExpr()

	if p.current.Type == token.IDENT && p.current.Literal == "if" {
		p.advance()
		forExpr.CondExpr = p.parseExpr()
	}

	if isObject {
		p.expect(token.RBRACE)
	} else {
		p.expect(token.RBRACKET)
	}
	return forExpr
}

func (p *Parser) parseConditional(pred ast.Expr) ast.Expr {
	pos := p.current.Pos
	p.advance() // consume ?
	trueExpr := p.parseExpr()
	p.expect(token.COLON)
	falseExpr := p.parseExpr()
	return &ast.CondExpr{Pred: pred, True: trueExpr, False: falseExpr, Pos: pos}
}
