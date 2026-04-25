package render

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/dgr237/tflens/pkg/analysis"
)

// expressionToAST walks an hclsyntax.Expression and returns a tagged
// JSON-serialisable tree describing its structure. The shape lets
// downstream converters (kro, crossplane, Pulumi, …) translate
// expressions without re-parsing the source text — every node is a
// `map[string]any` keyed on a `node` discriminator naming the
// expression kind, with the kind's specific fields beneath it.
//
// Nil-safe: returns nil when expr is nil.
//
// This is part of the experimental export schema. The supported node
// kinds are listed below; an unrecognised expression type falls back
// to `{node: "unknown", go_type: "<reflect type>"}` so the
// conversion at least surfaces the gap rather than silently dropping
// the expression — converters can always combine ast with the text
// field when they hit an unknown node.
//
// Supported nodes (one entry per hclsyntax expression type):
//
//	literal_value       - a constant cty.Value
//	template            - a template (interpolation) made of parts
//	template_wrap       - a single-expression interpolation, e.g. "${var.x}"
//	template_join       - the join of a for-expr template (rare)
//	scope_traversal     - a bare reference like var.x or aws_s3_bucket.b.id
//	relative_traversal  - chained traversal off another expression
//	function_call       - foo(arg1, arg2)
//	conditional         - cond ? a : b
//	binary_op           - a + b, a == b, etc.
//	unary_op            - !a, -a
//	index               - a[k]
//	splat               - aws_subnet.example[*].id
//	object_cons         - { a = 1, b = 2 }
//	tuple_cons          - [1, 2, 3]
//	parens              - (expr)
//	for                 - { for k, v in xs : k => v if cond }
func expressionToAST(expr hclsyntax.Expression) any {
	if expr == nil {
		return nil
	}
	switch e := expr.(type) {
	case *hclsyntax.LiteralValueExpr:
		return map[string]any{
			"node":  "literal_value",
			"value": ctyToExport(e.Val),
		}

	case *hclsyntax.TemplateExpr:
		// A bare quoted string with no interpolation parses as a
		// TemplateExpr with one LiteralValueExpr part — collapse to a
		// plain literal_value so consumers get the simpler shape for
		// the common case.
		if len(e.Parts) == 1 {
			if lit, ok := e.Parts[0].(*hclsyntax.LiteralValueExpr); ok {
				return map[string]any{
					"node":  "literal_value",
					"value": ctyToExport(lit.Val),
				}
			}
		}
		parts := make([]any, len(e.Parts))
		for i, p := range e.Parts {
			parts[i] = expressionToAST(p)
		}
		return map[string]any{"node": "template", "parts": parts}

	case *hclsyntax.TemplateWrapExpr:
		return map[string]any{
			"node":    "template_wrap",
			"wrapped": expressionToAST(e.Wrapped),
		}

	case *hclsyntax.TemplateJoinExpr:
		return map[string]any{
			"node":  "template_join",
			"tuple": expressionToAST(e.Tuple),
		}

	case *hclsyntax.ScopeTraversalExpr:
		return map[string]any{
			"node":      "scope_traversal",
			"traversal": traversalToAST(e.Traversal),
		}

	case *hclsyntax.RelativeTraversalExpr:
		return map[string]any{
			"node":      "relative_traversal",
			"source":    expressionToAST(e.Source),
			"traversal": traversalToAST(e.Traversal),
		}

	case *hclsyntax.FunctionCallExpr:
		args := make([]any, len(e.Args))
		for i, a := range e.Args {
			args[i] = expressionToAST(a)
		}
		out := map[string]any{
			"node": "function_call",
			"name": e.Name,
			"args": args,
		}
		if e.ExpandFinal {
			out["expand_final"] = true
		}
		return out

	case *hclsyntax.ConditionalExpr:
		return map[string]any{
			"node":      "conditional",
			"condition": expressionToAST(e.Condition),
			"true":      expressionToAST(e.TrueResult),
			"false":     expressionToAST(e.FalseResult),
		}

	case *hclsyntax.BinaryOpExpr:
		return map[string]any{
			"node": "binary_op",
			"op":   opName(e.Op),
			"lhs":  expressionToAST(e.LHS),
			"rhs":  expressionToAST(e.RHS),
		}

	case *hclsyntax.UnaryOpExpr:
		return map[string]any{
			"node":  "unary_op",
			"op":    opName(e.Op),
			"value": expressionToAST(e.Val),
		}

	case *hclsyntax.IndexExpr:
		return map[string]any{
			"node":       "index",
			"collection": expressionToAST(e.Collection),
			"key":        expressionToAST(e.Key),
		}

	case *hclsyntax.SplatExpr:
		return map[string]any{
			"node":   "splat",
			"source": expressionToAST(e.Source),
			"each":   expressionToAST(e.Each),
		}

	case *hclsyntax.ObjectConsExpr:
		items := make([]any, len(e.Items))
		for i, item := range e.Items {
			items[i] = map[string]any{
				"key":   expressionToAST(item.KeyExpr),
				"value": expressionToAST(item.ValueExpr),
			}
		}
		return map[string]any{"node": "object_cons", "items": items}

	case *hclsyntax.ObjectConsKeyExpr:
		// Object keys come wrapped to indicate whether they're a bare
		// identifier (default) or an explicit expression. Unwrap and
		// recurse — ScopeTraversalExpr inside conveys "bare-name key".
		return expressionToAST(e.Wrapped)

	case *hclsyntax.TupleConsExpr:
		elems := make([]any, len(e.Exprs))
		for i, x := range e.Exprs {
			elems[i] = expressionToAST(x)
		}
		return map[string]any{"node": "tuple_cons", "elements": elems}

	case *hclsyntax.ParenthesesExpr:
		return map[string]any{
			"node":  "parens",
			"value": expressionToAST(e.Expression),
		}

	case *hclsyntax.ForExpr:
		out := map[string]any{
			"node":         "for",
			"value_var":    e.ValVar,
			"collection":   expressionToAST(e.CollExpr),
			"value_result": expressionToAST(e.ValExpr),
		}
		if e.KeyVar != "" {
			out["key_var"] = e.KeyVar
		}
		if e.KeyExpr != nil {
			out["key_result"] = expressionToAST(e.KeyExpr)
		}
		if e.CondExpr != nil {
			out["cond"] = expressionToAST(e.CondExpr)
		}
		if e.Group {
			out["group"] = true
		}
		return out

	case *hclsyntax.AnonSymbolExpr:
		// Generated anonymous symbol — appears inside SplatExpr.Each
		// to bind the per-element value. Treated as opaque from the
		// AST consumer's perspective.
		return map[string]any{"node": "anon_symbol"}

	default:
		return map[string]any{
			"node":    "unknown",
			"go_type": fmt.Sprintf("%T", expr),
		}
	}
}

// traversalToAST converts an hcl.Traversal (a chain of
// root/attr/index/splat steps) into a JSON list. Used by both
// scope_traversal (`var.x.y`) and relative_traversal (`expr.attr.attr`)
// nodes — the chain shape is identical, only the source differs.
func traversalToAST(traversal hcl.Traversal) []any {
	out := make([]any, len(traversal))
	for i, step := range traversal {
		switch s := step.(type) {
		case hcl.TraverseRoot:
			out[i] = map[string]any{"step": "root", "name": s.Name}
		case hcl.TraverseAttr:
			out[i] = map[string]any{"step": "attr", "name": s.Name}
		case hcl.TraverseIndex:
			out[i] = map[string]any{"step": "index", "key": ctyToExport(s.Key)}
		case hcl.TraverseSplat:
			out[i] = map[string]any{"step": "splat"}
		default:
			out[i] = map[string]any{"step": "unknown", "go_type": fmt.Sprintf("%T", step)}
		}
	}
	return out
}

// opName returns a stable symbolic name for an hclsyntax.Operation,
// matching the source-syntax operator (`+`, `==`, `&&`, …). Used
// for binary_op and unary_op nodes so consumers don't need to import
// hclsyntax to interpret the op.
func opName(op *hclsyntax.Operation) string {
	switch op {
	case hclsyntax.OpAdd:
		return "+"
	case hclsyntax.OpSubtract:
		return "-"
	case hclsyntax.OpMultiply:
		return "*"
	case hclsyntax.OpDivide:
		return "/"
	case hclsyntax.OpModulo:
		return "%"
	case hclsyntax.OpNegate:
		return "-"
	case hclsyntax.OpEqual:
		return "=="
	case hclsyntax.OpNotEqual:
		return "!="
	case hclsyntax.OpGreaterThan:
		return ">"
	case hclsyntax.OpGreaterThanOrEqual:
		return ">="
	case hclsyntax.OpLessThan:
		return "<"
	case hclsyntax.OpLessThanOrEqual:
		return "<="
	case hclsyntax.OpLogicalAnd:
		return "&&"
	case hclsyntax.OpLogicalOr:
		return "||"
	case hclsyntax.OpLogicalNot:
		return "!"
	default:
		return "unknown"
	}
}

// astFor builds the AST for an analysis.Expr — convenience wrapper
// that pulls the underlying hclsyntax.Expression out and runs it
// through expressionToAST. Returns nil for a nil/empty *Expr.
func astFor(e *analysis.Expr) any {
	if e == nil || e.E == nil {
		return nil
	}
	return expressionToAST(e.E)
}
