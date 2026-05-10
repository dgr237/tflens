package render

import (
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/dgr237/tflens/pkg/analysis"
)

// ExportCountKind classifies a `count = ...` expression at export
// time so consumers don't have to keep parallel classifiers in sync.
// Three kinds:
//
//   - "include_when":  count = <cond> ? 1 : 0  (or  ? 0 : 1, with the
//     condition logically negated). Surfaces ConditionText so the
//     downstream rewrite to a target system's "include this resource
//     if <cond>" primitive doesn't need to walk the AST.
//   - "scalar":        count = <number literal> (e.g. count = 3).
//     CountExprText carries the literal text.
//   - "expression":    anything else — count = length(var.subnets),
//     count = var.replica_count, etc. CountExprText carries the source
//     text; consumers fall back to the AST for structural decoding.
//
// Kept separate from the count expression itself rather than replacing
// it, so consumers wanting raw text/value/ast continue to read Count
// without changes.
type ExportCountKind struct {
	Kind          string `json:"kind"`
	ConditionText string `json:"condition_text,omitempty"`
	CountExprText string `json:"count_expr_text,omitempty"`
}

// classifyCount inspects the AST of a count expression. Returns nil
// for a nil expression so the omitempty tag drops the field for
// resources / module calls without `count`.
//
// The include_when classification accepts both `cond ? 1 : 0` and
// `cond ? 0 : 1` (the second flips the condition's polarity by
// negation in ConditionText) so refactors that swap the literal order
// stay equivalent under this shape.
func classifyCount(e *analysis.Expr) *ExportCountKind {
	if e == nil || e.E == nil {
		return nil
	}
	out := &ExportCountKind{CountExprText: e.Text()}

	if cond, ok := e.E.(*hclsyntax.ConditionalExpr); ok {
		t, tOK := literalNumber(cond.TrueResult)
		f, fOK := literalNumber(cond.FalseResult)
		if tOK && fOK {
			condExpr := &analysis.Expr{E: cond.Condition, Source: e.Source}
			switch {
			case t == 1 && f == 0:
				out.Kind = "include_when"
				out.ConditionText = condExpr.Text()
				return out
			case t == 0 && f == 1:
				out.Kind = "include_when"
				out.ConditionText = "!(" + condExpr.Text() + ")"
				return out
			}
		}
	}

	if n, ok := literalNumber(e.E); ok {
		out.Kind = "scalar"
		// Re-emit the canonical literal text (e.Text() already does the
		// hclwrite normalisation) — set CountExprText explicitly to a
		// trimmed form just so consumers don't see surrounding parens.
		_ = n
		return out
	}

	out.Kind = "expression"
	return out
}

// literalNumber returns the int64 value of a literal-number expression.
// hclsyntax represents numeric literals as LiteralValueExpr wrapping a
// cty.Number; templates / interpolations don't qualify. Returns
// (0, false) for anything else.
func literalNumber(expr hclsyntax.Expression) (int64, bool) {
	lit, ok := expr.(*hclsyntax.LiteralValueExpr)
	if !ok {
		return 0, false
	}
	if lit.Val.IsNull() || !lit.Val.IsKnown() || lit.Val.Type() != cty.Number {
		return 0, false
	}
	bf := lit.Val.AsBigFloat()
	if !bf.IsInt() {
		return 0, false
	}
	n, _ := bf.Int64()
	return n, true
}
