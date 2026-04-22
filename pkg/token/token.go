package token

import "fmt"

// TokenType identifies the type of a lexed token.
type TokenType int

const (
	ILLEGAL TokenType = iota
	EOF
	NEWLINE

	// Identifiers and literals
	IDENT
	NUMBER
	STRING
	BOOL
	NULL
	HEREDOC

	// Trivia
	COMMENT // # ... or // ... or /* ... */

	// Delimiters
	LBRACE
	RBRACE
	LBRACKET
	RBRACKET
	LPAREN
	RPAREN

	// Structural operators
	EQUALS
	COMMA
	DOT
	STAR
	QUESTION
	COLON
	FAT_ARROW      // =>
	ELLIPSIS       // ...
	TEMPLATE_START // ${

	// Arithmetic
	PLUS
	MINUS
	SLASH
	PERCENT

	// Comparison
	EQ_EQ   // ==
	BANG_EQ // !=
	LT
	GT
	LT_EQ // <=
	GT_EQ // >=

	// Logical
	AND_AND // &&
	OR_OR   // ||
	BANG    // !
)

var tokenNames = map[TokenType]string{
	ILLEGAL:        "ILLEGAL",
	EOF:            "EOF",
	NEWLINE:        "NEWLINE",
	IDENT:          "IDENT",
	NUMBER:         "NUMBER",
	STRING:         "STRING",
	BOOL:           "BOOL",
	NULL:           "NULL",
	HEREDOC:        "HEREDOC",
	COMMENT:        "COMMENT",
	LBRACE:         "LBRACE",
	RBRACE:         "RBRACE",
	LBRACKET:       "LBRACKET",
	RBRACKET:       "RBRACKET",
	LPAREN:         "LPAREN",
	RPAREN:         "RPAREN",
	EQUALS:         "EQUALS",
	COMMA:          "COMMA",
	DOT:            "DOT",
	STAR:           "STAR",
	QUESTION:       "QUESTION",
	COLON:          "COLON",
	FAT_ARROW:      "FAT_ARROW",
	ELLIPSIS:       "ELLIPSIS",
	TEMPLATE_START: "TEMPLATE_START",
	PLUS:           "PLUS",
	MINUS:          "MINUS",
	SLASH:          "SLASH",
	PERCENT:        "PERCENT",
	EQ_EQ:          "EQ_EQ",
	BANG_EQ:        "BANG_EQ",
	LT:             "LT",
	GT:             "GT",
	LT_EQ:          "LT_EQ",
	GT_EQ:          "GT_EQ",
	AND_AND:        "AND_AND",
	OR_OR:          "OR_OR",
	BANG:           "BANG",
}

func (t TokenType) String() string {
	if name, ok := tokenNames[t]; ok {
		return name
	}
	return fmt.Sprintf("TokenType(%d)", int(t))
}

// Position describes a source location.
type Position struct {
	File   string
	Line   int
	Column int
}

func (p Position) String() string {
	if p.File != "" {
		return fmt.Sprintf("%s:%d:%d", p.File, p.Line, p.Column)
	}
	return fmt.Sprintf("%d:%d", p.Line, p.Column)
}

// Token is a single lexed unit.
type Token struct {
	Type    TokenType
	Literal string
	Pos     Position
}

func (t Token) String() string {
	return fmt.Sprintf("Token{%s %q %s}", t.Type, t.Literal, t.Pos)
}
