package ast

import "github.com/dgr237/tflens/pkg/token"

// Node is implemented by every AST node.
type Node interface {
	nodePos() token.Position
}

// Expr is implemented by every expression node.
type Expr interface {
	Node
	exprNode()
}

// ---- Top-level ----

type File struct {
	Body *Body
	Pos  token.Position
}

func (f *File) nodePos() token.Position { return f.Pos }

// Body holds the contents of a file or block: a sequence of blocks and attributes.
type Body struct {
	Nodes []Node
	Pos   token.Position
	// TrailingComments are comments that appear after the last statement in
	// the body but before its close (EOF for a File, "}" for a block).
	TrailingComments []string
}

func (b *Body) nodePos() token.Position { return b.Pos }

// ---- Structural nodes ----

// Block represents a labeled block: resource "aws_instance" "web" { ... }
type Block struct {
	Type            string
	Labels          []string
	Body            *Body
	Pos             token.Position
	LeadingComments []string // full comment text including # / // / /* */
	TrailingComment string   // inline comment on the block's opening line
}

func (b *Block) nodePos() token.Position { return b.Pos }

// Attribute represents a single assignment: name = expr
type Attribute struct {
	Name            string
	Value           Expr
	Pos             token.Position
	LeadingComments []string
	TrailingComment string
}

func (a *Attribute) nodePos() token.Position { return a.Pos }

// ---- Expression nodes ----

// LiteralExpr holds a scalar literal value.
// Value is one of: string, float64, bool, nil (for null).
type LiteralExpr struct {
	Value any
	Pos   token.Position
}

func (e *LiteralExpr) nodePos() token.Position { return e.Pos }
func (e *LiteralExpr) exprNode()               {}

// RefExpr is a traversal reference: aws_instance.web.id
type RefExpr struct {
	Parts []string
	Pos   token.Position
}

func (e *RefExpr) nodePos() token.Position { return e.Pos }
func (e *RefExpr) exprNode()               {}

// IndexExpr is a collection index: list[0] or map["key"]
type IndexExpr struct {
	Collection Expr
	Key        Expr
	Pos        token.Position
}

func (e *IndexExpr) nodePos() token.Position { return e.Pos }
func (e *IndexExpr) exprNode()               {}

// SplatExpr is a splat: aws_instance.web[*].id or aws_instance.web.*.id
type SplatExpr struct {
	Source Expr
	Each   Expr
	Pos    token.Position
}

func (e *SplatExpr) nodePos() token.Position { return e.Pos }
func (e *SplatExpr) exprNode()               {}

// CallExpr is a function call: tostring(42)
type CallExpr struct {
	Name       string
	Args       []Expr
	ExpandLast bool // true when last arg has ...
	Pos        token.Position
}

func (e *CallExpr) nodePos() token.Position { return e.Pos }
func (e *CallExpr) exprNode()               {}

// TemplateExpr is a quoted template: "hello ${var.name}"
type TemplateExpr struct {
	Parts []TemplatePart
	Pos   token.Position
}

// TemplatePart is either a literal string segment or an interpolated expression.
type TemplatePart struct {
	IsLiteral bool
	Literal   string
	Expr      Expr
}

func (e *TemplateExpr) nodePos() token.Position { return e.Pos }
func (e *TemplateExpr) exprNode()               {}

// TupleExpr is a list literal: [a, b, c]
type TupleExpr struct {
	Items []Expr
	Pos   token.Position
}

func (e *TupleExpr) nodePos() token.Position { return e.Pos }
func (e *TupleExpr) exprNode()               {}

// ObjectExpr is an object literal: { key = val }
type ObjectExpr struct {
	Items []ObjectItem
	Pos   token.Position
}

// ObjectItem is a single key/value pair in an ObjectExpr.
type ObjectItem struct {
	Key   Expr
	Value Expr
}

func (e *ObjectExpr) nodePos() token.Position { return e.Pos }
func (e *ObjectExpr) exprNode()               {}

// ForExpr is a for expression.
// List form:  [for v in collection : expr if cond]
// Object form: {for k, v in collection : keyExpr => valExpr if cond}
type ForExpr struct {
	KeyVar   string // empty for list form
	ValVar   string
	CollExpr Expr
	KeyExpr  Expr // nil for list form
	ValExpr  Expr
	CondExpr Expr // nil when there is no if clause
	Pos      token.Position
}

func (e *ForExpr) nodePos() token.Position { return e.Pos }
func (e *ForExpr) exprNode()               {}

// CondExpr is a conditional expression: pred ? trueVal : falseVal
type CondExpr struct {
	Pred  Expr
	True  Expr
	False Expr
	Pos   token.Position
}

func (e *CondExpr) nodePos() token.Position { return e.Pos }
func (e *CondExpr) exprNode()               {}

// BinaryExpr is a binary operation: left op right
// Op is the operator literal: +, -, *, /, %, ==, !=, <, >, <=, >=, &&, ||
type BinaryExpr struct {
	Op    string
	Left  Expr
	Right Expr
	Pos   token.Position
}

func (e *BinaryExpr) nodePos() token.Position { return e.Pos }
func (e *BinaryExpr) exprNode()               {}

// UnaryExpr is a unary operation: op operand
// Op is the operator literal: !, -
type UnaryExpr struct {
	Op      string
	Operand Expr
	Pos     token.Position
}

func (e *UnaryExpr) nodePos() token.Position { return e.Pos }
func (e *UnaryExpr) exprNode()               {}
