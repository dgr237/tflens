package lexer

import (
	"bytes"
	"fmt"
	"strings"
	"github.com/dgr237/tflens/pkg/token"
)

type lexMode int

const (
	modeNormal   lexMode = iota
	modeString           // inside "..."
	modeTemplate         // inside ${ } within a string
)

// Lexer tokenises a single HCL/Terraform source file.
type Lexer struct {
	src      []byte
	pos      int
	line     int
	col      int
	filename string
	modes    []lexMode

	// depth tracks open (, [, { while in modeNormal or modeTemplate
	// so we can suppress NEWLINEs inside multi-line constructs.
	depth int
}

// New creates a Lexer for src. filename is used in Position values only.
func New(src []byte, filename string) *Lexer {
	return &Lexer{
		src:      src,
		pos:      0,
		line:     1,
		col:      1,
		filename: filename,
		modes:    []lexMode{modeNormal},
	}
}

// Tokens returns all tokens from the source, including the final EOF.
func (l *Lexer) Tokens() []token.Token {
	var tokens []token.Token
	for {
		t := l.Next()
		tokens = append(tokens, t)
		if t.Type == token.EOF {
			break
		}
	}
	return tokens
}

// Next returns the next token.
func (l *Lexer) Next() token.Token {
	switch l.currentMode() {
	case modeString:
		return l.nextStringToken()
	default:
		return l.nextNormalToken()
	}
}

// ---- mode helpers ----

func (l *Lexer) currentMode() lexMode {
	if len(l.modes) == 0 {
		return modeNormal
	}
	return l.modes[len(l.modes)-1]
}

func (l *Lexer) pushMode(m lexMode) { l.modes = append(l.modes, m) }
func (l *Lexer) popMode() {
	if len(l.modes) > 1 {
		l.modes = l.modes[:len(l.modes)-1]
	}
}

// ---- character helpers ----

func (l *Lexer) ch() byte {
	if l.pos >= len(l.src) {
		return 0
	}
	return l.src[l.pos]
}

func (l *Lexer) peek() byte {
	if l.pos+1 >= len(l.src) {
		return 0
	}
	return l.src[l.pos+1]
}

func (l *Lexer) advance() byte {
	ch := l.ch()
	l.pos++
	if ch == '\n' {
		l.line++
		l.col = 1
	} else {
		l.col++
	}
	return ch
}

func (l *Lexer) pos2() token.Position {
	return token.Position{File: l.filename, Line: l.line, Column: l.col}
}

func (l *Lexer) makeToken(t token.TokenType, lit string, pos token.Position) token.Token {
	return token.Token{Type: t, Literal: lit, Pos: pos}
}

// ---- normal/template mode lexing ----

func (l *Lexer) nextNormalToken() token.Token {
	l.skipWhitespace()

	if l.pos >= len(l.src) {
		return l.makeToken(token.EOF, "", l.pos2())
	}

	pos := l.pos2()
	ch := l.ch()

	// Comments
	if ch == '#' || (ch == '/' && l.peek() == '/') {
		return l.readLineComment(pos)
	}
	if ch == '/' && l.peek() == '*' {
		return l.readBlockComment(pos)
	}

	// Newline — only emit when not inside brackets/parens/braces
	if ch == '\n' {
		l.advance()
		if l.depth == 0 {
			return l.makeToken(token.NEWLINE, "\n", pos)
		}
		return l.nextNormalToken()
	}

	// Heredoc: <<EOF or <<-EOF
	if ch == '<' && l.peek() == '<' {
		return l.readHeredoc(pos)
	}

	// String
	if ch == '"' {
		l.advance() // consume opening "
		l.pushMode(modeString)
		// Return the first string token immediately by switching mode
		return l.nextStringToken()
	}

	// Numbers (negative sign is handled as unary minus in the parser)
	if isDigit(ch) {
		return l.readNumber(pos)
	}

	// Identifiers, keywords
	if isLetter(ch) || ch == '_' {
		return l.readIdent(pos)
	}

	// Single and multi-char operators
	l.advance()
	switch ch {
	case '{':
		l.depth++
		return l.makeToken(token.LBRACE, "{", pos)
	case '}':
		if l.currentMode() == modeTemplate {
			l.popMode() // back to modeString
			return l.makeToken(token.RBRACE, "}", pos)
		}
		if l.depth > 0 {
			l.depth--
		}
		return l.makeToken(token.RBRACE, "}", pos)
	case '[':
		l.depth++
		return l.makeToken(token.LBRACKET, "[", pos)
	case ']':
		if l.depth > 0 {
			l.depth--
		}
		return l.makeToken(token.RBRACKET, "]", pos)
	case '(':
		l.depth++
		return l.makeToken(token.LPAREN, "(", pos)
	case ')':
		if l.depth > 0 {
			l.depth--
		}
		return l.makeToken(token.RPAREN, ")", pos)
	case '=':
		if l.ch() == '>' {
			l.advance()
			return l.makeToken(token.FAT_ARROW, "=>", pos)
		}
		if l.ch() == '=' {
			l.advance()
			return l.makeToken(token.EQ_EQ, "==", pos)
		}
		return l.makeToken(token.EQUALS, "=", pos)
	case '!':
		if l.ch() == '=' {
			l.advance()
			return l.makeToken(token.BANG_EQ, "!=", pos)
		}
		return l.makeToken(token.BANG, "!", pos)
	case '<':
		if l.ch() == '=' {
			l.advance()
			return l.makeToken(token.LT_EQ, "<=", pos)
		}
		return l.makeToken(token.LT, "<", pos)
	case '>':
		if l.ch() == '=' {
			l.advance()
			return l.makeToken(token.GT_EQ, ">=", pos)
		}
		return l.makeToken(token.GT, ">", pos)
	case '&':
		if l.ch() == '&' {
			l.advance()
			return l.makeToken(token.AND_AND, "&&", pos)
		}
		return l.makeToken(token.ILLEGAL, "&", pos)
	case '|':
		if l.ch() == '|' {
			l.advance()
			return l.makeToken(token.OR_OR, "||", pos)
		}
		return l.makeToken(token.ILLEGAL, "|", pos)
	case '+':
		return l.makeToken(token.PLUS, "+", pos)
	case '-':
		return l.makeToken(token.MINUS, "-", pos)
	case '/':
		return l.makeToken(token.SLASH, "/", pos)
	case '%':
		return l.makeToken(token.PERCENT, "%", pos)
	case ',':
		return l.makeToken(token.COMMA, ",", pos)
	case '.':
		if l.ch() == '.' && l.peek() == '.' {
			l.advance()
			l.advance()
			return l.makeToken(token.ELLIPSIS, "...", pos)
		}
		return l.makeToken(token.DOT, ".", pos)
	case '*':
		return l.makeToken(token.STAR, "*", pos)
	case '?':
		return l.makeToken(token.QUESTION, "?", pos)
	case ':':
		return l.makeToken(token.COLON, ":", pos)
	}

	return l.makeToken(token.ILLEGAL, string(ch), pos)
}

// ---- string mode lexing ----

// nextStringToken reads from inside a quoted string, emitting either a
// STRING literal segment, a TEMPLATE_START when ${ is seen, or EOF/NEWLINE
// on closing quote.
func (l *Lexer) nextStringToken() token.Token {
	pos := l.pos2()
	var buf bytes.Buffer

	for l.pos < len(l.src) {
		ch := l.ch()

		if ch == '"' {
			l.advance() // consume closing "
			l.popMode()
			return l.makeToken(token.STRING, buf.String(), pos)
		}

		if ch == '$' && l.peek() == '{' {
			if buf.Len() > 0 {
				// Emit the literal segment we've accumulated before the ${
				return l.makeToken(token.STRING, buf.String(), pos)
			}
			l.advance() // $
			l.advance() // {
			l.pushMode(modeTemplate)
			l.depth++
			return l.makeToken(token.TEMPLATE_START, "${", pos)
		}

		if ch == '\\' {
			buf.WriteByte(l.readEscape())
			continue
		}

		buf.WriteByte(l.advance())
	}

	// Unterminated string
	return l.makeToken(token.ILLEGAL, buf.String(), pos)
}

func (l *Lexer) readEscape() byte {
	l.advance() // consume backslash
	switch l.ch() {
	case 'n':
		l.advance()
		return '\n'
	case 't':
		l.advance()
		return '\t'
	case 'r':
		l.advance()
		return '\r'
	case '"':
		l.advance()
		return '"'
	case '\\':
		l.advance()
		return '\\'
	default:
		return l.advance()
	}
}

// ---- readers ----

func (l *Lexer) readIdent(pos token.Position) token.Token {
	start := l.pos
	for isLetter(l.ch()) || isDigit(l.ch()) || l.ch() == '_' || l.ch() == '-' {
		l.advance()
	}
	lit := string(l.src[start:l.pos])
	switch lit {
	case "true", "false":
		return l.makeToken(token.BOOL, lit, pos)
	case "null":
		return l.makeToken(token.NULL, lit, pos)
	}
	return l.makeToken(token.IDENT, lit, pos)
}

func (l *Lexer) readNumber(pos token.Position) token.Token {
	start := l.pos
	for isDigit(l.ch()) {
		l.advance()
	}
	if l.ch() == '.' && isDigit(l.peek()) {
		l.advance()
		for isDigit(l.ch()) {
			l.advance()
		}
	}
	// optional exponent
	if l.ch() == 'e' || l.ch() == 'E' {
		l.advance()
		if l.ch() == '+' || l.ch() == '-' {
			l.advance()
		}
		for isDigit(l.ch()) {
			l.advance()
		}
	}
	return l.makeToken(token.NUMBER, string(l.src[start:l.pos]), pos)
}

func (l *Lexer) readHeredoc(pos token.Position) token.Token {
	l.advance() // first <
	l.advance() // second <

	strip := false
	if l.ch() == '-' {
		strip = true
		l.advance()
	}

	// Read marker
	var marker bytes.Buffer
	for l.ch() != '\n' && l.pos < len(l.src) {
		marker.WriteByte(l.advance())
	}
	if l.ch() == '\n' {
		l.advance()
	}
	markerStr := strings.TrimSpace(marker.String())

	// Read body until a line that is exactly the marker
	var body bytes.Buffer
	for l.pos < len(l.src) {
		lineStart := l.pos
		var line bytes.Buffer
		for l.ch() != '\n' && l.pos < len(l.src) {
			line.WriteByte(l.advance())
		}
		if l.ch() == '\n' {
			l.advance()
		}
		trimmed := strings.TrimLeft(line.String(), "\t ")
		if trimmed == markerStr {
			break
		}
		_ = lineStart
		if strip {
			body.WriteString(trimmed + "\n")
		} else {
			body.WriteString(line.String() + "\n")
		}
	}

	lit := fmt.Sprintf("<<%s\n%s%s", markerStr, body.String(), markerStr)
	return l.makeToken(token.HEREDOC, lit, pos)
}

// ---- skip helpers ----

func (l *Lexer) skipWhitespace() {
	for l.pos < len(l.src) {
		ch := l.ch()
		if ch == ' ' || ch == '\t' || ch == '\r' {
			l.advance()
		} else {
			break
		}
	}
}

// readLineComment consumes a # or // line comment up to (but not including)
// the terminating newline. The resulting COMMENT literal preserves the
// comment's leading marker so the printer can emit it verbatim.
func (l *Lexer) readLineComment(pos token.Position) token.Token {
	start := l.pos
	for l.pos < len(l.src) && l.ch() != '\n' {
		l.advance()
	}
	return l.makeToken(token.COMMENT, string(l.src[start:l.pos]), pos)
}

// readBlockComment consumes a /* ... */ block comment. The literal includes
// the opening /* and closing */.
func (l *Lexer) readBlockComment(pos token.Position) token.Token {
	start := l.pos
	l.advance() // /
	l.advance() // *
	for l.pos < len(l.src) {
		if l.ch() == '*' && l.peek() == '/' {
			l.advance()
			l.advance()
			return l.makeToken(token.COMMENT, string(l.src[start:l.pos]), pos)
		}
		l.advance()
	}
	// Unterminated: return what we have.
	return l.makeToken(token.COMMENT, string(l.src[start:l.pos]), pos)
}

// ---- character class helpers ----

func isLetter(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') || (ch >= 'A' && ch <= 'Z')
}

func isDigit(ch byte) bool {
	return ch >= '0' && ch <= '9'
}
