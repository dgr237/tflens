package ast

import "github.com/dgr237/tflens/pkg/token"

// NodePos returns the source position of any AST node.
func NodePos(n Node) token.Position {
	if n == nil {
		return token.Position{}
	}
	return n.nodePos()
}

// Visitor is called for each node during a Walk.
// If Visit returns nil, the node's children are not visited.
type Visitor interface {
	Visit(node Node) Visitor
}

// Walk performs a depth-first traversal of the AST, calling v.Visit on each node.
func Walk(v Visitor, node Node) {
	if node == nil {
		return
	}
	w := v.Visit(node)
	if w == nil {
		return
	}

	switch n := node.(type) {
	case *File:
		Walk(w, n.Body)

	case *Body:
		for _, child := range n.Nodes {
			Walk(w, child)
		}

	case *Block:
		Walk(w, n.Body)

	case *Attribute:
		walkExpr(w, n.Value)

	case *LiteralExpr:
		// leaf

	case *RefExpr:
		// leaf

	case *IndexExpr:
		walkExpr(w, n.Collection)
		walkExpr(w, n.Key)

	case *SplatExpr:
		walkExpr(w, n.Source)
		walkExpr(w, n.Each)

	case *CallExpr:
		for _, arg := range n.Args {
			walkExpr(w, arg)
		}

	case *TemplateExpr:
		for _, part := range n.Parts {
			if !part.IsLiteral {
				walkExpr(w, part.Expr)
			}
		}

	case *TupleExpr:
		for _, item := range n.Items {
			walkExpr(w, item)
		}

	case *ObjectExpr:
		for _, item := range n.Items {
			walkExpr(w, item.Key)
			walkExpr(w, item.Value)
		}

	case *ForExpr:
		walkExpr(w, n.CollExpr)
		if n.KeyExpr != nil {
			walkExpr(w, n.KeyExpr)
		}
		walkExpr(w, n.ValExpr)
		if n.CondExpr != nil {
			walkExpr(w, n.CondExpr)
		}

	case *CondExpr:
		walkExpr(w, n.Pred)
		walkExpr(w, n.True)
		walkExpr(w, n.False)

	case *BinaryExpr:
		walkExpr(w, n.Left)
		walkExpr(w, n.Right)

	case *UnaryExpr:
		walkExpr(w, n.Operand)
	}
}

func walkExpr(v Visitor, e Expr) {
	if e != nil {
		Walk(v, e)
	}
}

// Inspect traverses the AST calling f on each node.
// If f returns false the node's children are skipped.
// Modelled after go/ast.Inspect.
func Inspect(node Node, f func(Node) bool) {
	Walk(inspectVisitor(f), node)
}

type inspectVisitor func(Node) bool

func (fn inspectVisitor) Visit(node Node) Visitor {
	if fn(node) {
		return fn
	}
	return nil
}
