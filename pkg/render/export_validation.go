package render

import (
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/dgr237/tflens/pkg/analysis"
)

// ExportValidationFolded is the named-shape hint emitted on
// `validation { condition = ... }` blocks. Saves downstream converters
// from re-implementing the buildModifiers pattern matching against the
// AST. Falls back to {kind: "complex"} when no recognised pattern
// matches — the AST and condition text remain authoritative.
//
// Recognised kinds and their params:
//
//	enum:          { values: [...literal values...] }
//	min_length:    { min: <int> }
//	max_length:    { max: <int> }
//	length_range:  { min?: <int>, max?: <int> }
//	minimum:       { min: <number> }
//	maximum:       { max: <number> }
//	pattern:       { regex: "<pattern>" }
//	complex:       (no params — the condition didn't match a known shape)
type ExportValidationFolded struct {
	Kind   string         `json:"kind"`
	Params map[string]any `json:"params,omitempty"`
}

// foldValidation pattern-matches condition against the recognised
// validation shapes. Returns nil only when there's no condition at
// all; every other input gets at least {kind: "complex"} so the
// folded field is uniformly populated for validation blocks (the
// presence of the field signals "this is a validation, not a
// pre/post-condition" beyond the per-block location).
func foldValidation(cond *analysis.Expr) *ExportValidationFolded {
	if cond == nil || cond.E == nil {
		return nil
	}
	if f := foldEnum(cond.E); f != nil {
		return f
	}
	if f := foldLength(cond.E); f != nil {
		return f
	}
	if f := foldNumericBound(cond.E); f != nil {
		return f
	}
	if f := foldRegex(cond.E); f != nil {
		return f
	}
	return &ExportValidationFolded{Kind: "complex"}
}

// foldEnum recognises `contains([<lit>, <lit>, ...], <something>)` as
// an enum constraint. The bare-tuple-of-literals shape is the JSON-
// schema-style enum that downstream schemas can render directly. The
// "<something>" subject (typically `var.X`) isn't checked — validations
// always live on a single variable, and the subject is implied.
func foldEnum(expr hclsyntax.Expression) *ExportValidationFolded {
	call, ok := expr.(*hclsyntax.FunctionCallExpr)
	if !ok || call.Name != "contains" || len(call.Args) != 2 {
		return nil
	}
	tup, ok := call.Args[0].(*hclsyntax.TupleConsExpr)
	if !ok {
		return nil
	}
	values := make([]any, 0, len(tup.Exprs))
	for _, item := range tup.Exprs {
		v, diags := item.Value(nil)
		if diags.HasErrors() || v.IsNull() || !v.IsKnown() {
			return nil
		}
		values = append(values, ctyValueToInterface(v))
	}
	return &ExportValidationFolded{
		Kind:   "enum",
		Params: map[string]any{"values": values},
	}
}

// foldLength recognises five shapes:
//
//	length(<x>) >= <n>           → min_length
//	length(<x>) <= <n>           → max_length
//	length(<x>) == <n>           → length_range with min == max == n
//	length(<x>) >= <a> && length(<x>) <= <b>   → length_range
//	length(<x>) <= <b> && length(<x>) >= <a>   → length_range
//
// The conjunction shape is a single BinaryOpExpr with Op = OpLogicalAnd
// whose LHS / RHS are themselves recognisable bound shapes. The
// equality shape is a degenerate range collapsing to a single fixed
// length — composegen's docs (§4.1) treat it identically to
// minLength=N + maxLength=N. Only the bounds are surfaced; the subject
// `<x>` isn't checked (always var.X in practice).
func foldLength(expr hclsyntax.Expression) *ExportValidationFolded {
	if and, ok := expr.(*hclsyntax.BinaryOpExpr); ok && and.Op == hclsyntax.OpLogicalAnd {
		left := lengthBound(and.LHS)
		right := lengthBound(and.RHS)
		if left != nil && right != nil {
			params := map[string]any{}
			for _, b := range []*lengthBoundResult{left, right} {
				if b.isMin {
					params["min"] = b.n
				} else {
					params["max"] = b.n
				}
			}
			if len(params) == 2 {
				return &ExportValidationFolded{Kind: "length_range", Params: params}
			}
		}
	}
	if n, ok := lengthEquality(expr); ok {
		return &ExportValidationFolded{
			Kind:   "length_range",
			Params: map[string]any{"min": n, "max": n},
		}
	}
	if b := lengthBound(expr); b != nil {
		if b.isMin {
			return &ExportValidationFolded{Kind: "min_length", Params: map[string]any{"min": b.n}}
		}
		return &ExportValidationFolded{Kind: "max_length", Params: map[string]any{"max": b.n}}
	}
	return nil
}

// lengthEquality matches `length(<x>) == <n>` and returns (n, true).
// Folds to length_range with min == max so the wire shape stays
// consistent with the bounded forms — consumers expressing the
// constraint as a JSON-schema length range get a uniform shape
// regardless of whether the source used `==` or `>= && <=`.
func lengthEquality(expr hclsyntax.Expression) (int64, bool) {
	bin, ok := expr.(*hclsyntax.BinaryOpExpr)
	if !ok || bin.Op != hclsyntax.OpEqual {
		return 0, false
	}
	call, ok := bin.LHS.(*hclsyntax.FunctionCallExpr)
	if !ok || call.Name != "length" || len(call.Args) != 1 {
		return 0, false
	}
	return literalNumber(bin.RHS)
}

type lengthBoundResult struct {
	isMin bool
	n     int64
}

// lengthBound matches `length(<x>) >= <n>` (isMin=true), `length(<x>)
// <= <n>` (isMin=false), and the strict variants `>` / `<` adjusted
// by ±1. Returns nil when the expression isn't a recognisable length
// bound. The strict-variant adjustment lets `length(x) > 0` fold to
// min_length=1, which matches what schemas typically express.
func lengthBound(expr hclsyntax.Expression) *lengthBoundResult {
	bin, ok := expr.(*hclsyntax.BinaryOpExpr)
	if !ok {
		return nil
	}
	call, ok := bin.LHS.(*hclsyntax.FunctionCallExpr)
	if !ok || call.Name != "length" || len(call.Args) != 1 {
		return nil
	}
	n, ok := literalNumber(bin.RHS)
	if !ok {
		return nil
	}
	switch bin.Op {
	case hclsyntax.OpGreaterThanOrEqual:
		return &lengthBoundResult{isMin: true, n: n}
	case hclsyntax.OpGreaterThan:
		return &lengthBoundResult{isMin: true, n: n + 1}
	case hclsyntax.OpLessThanOrEqual:
		return &lengthBoundResult{isMin: false, n: n}
	case hclsyntax.OpLessThan:
		return &lengthBoundResult{isMin: false, n: n - 1}
	}
	return nil
}

// foldNumericBound recognises `<x> >= <n>` / `<x> <= <n>` /
// `<x> > <n>` / `<x> < <n>` where LHS isn't `length(...)` (those go
// through foldLength). Mirrors the length variants' strict-bound
// adjustment so `var.x > 0` folds to minimum=1.
func foldNumericBound(expr hclsyntax.Expression) *ExportValidationFolded {
	bin, ok := expr.(*hclsyntax.BinaryOpExpr)
	if !ok {
		return nil
	}
	if call, ok := bin.LHS.(*hclsyntax.FunctionCallExpr); ok && call.Name == "length" {
		return nil // length bounds are folded separately
	}
	n, ok := literalNumber(bin.RHS)
	if !ok {
		return nil
	}
	switch bin.Op {
	case hclsyntax.OpGreaterThanOrEqual:
		return &ExportValidationFolded{Kind: "minimum", Params: map[string]any{"min": n}}
	case hclsyntax.OpGreaterThan:
		return &ExportValidationFolded{Kind: "minimum", Params: map[string]any{"min": n + 1}}
	case hclsyntax.OpLessThanOrEqual:
		return &ExportValidationFolded{Kind: "maximum", Params: map[string]any{"max": n}}
	case hclsyntax.OpLessThan:
		return &ExportValidationFolded{Kind: "maximum", Params: map[string]any{"max": n - 1}}
	}
	return nil
}

// foldRegex recognises three regex shapes:
//
//	can(regex("<pattern>", <x>))
//	regex("<pattern>", <x>) != null     (rare; included for completeness)
//	length(regexall("<pattern>", <x>)) > 0
//
// Only the first is common in real code; the others are listed in the
// downstream classifier's TODO list, so we cover them here too.
func foldRegex(expr hclsyntax.Expression) *ExportValidationFolded {
	if call, ok := expr.(*hclsyntax.FunctionCallExpr); ok && call.Name == "can" && len(call.Args) == 1 {
		if pattern, ok := regexCallPattern(call.Args[0]); ok {
			return &ExportValidationFolded{Kind: "pattern", Params: map[string]any{"regex": pattern}}
		}
	}
	if bin, ok := expr.(*hclsyntax.BinaryOpExpr); ok && bin.Op == hclsyntax.OpGreaterThan {
		if call, ok := bin.LHS.(*hclsyntax.FunctionCallExpr); ok && call.Name == "length" && len(call.Args) == 1 {
			if inner, ok := call.Args[0].(*hclsyntax.FunctionCallExpr); ok && inner.Name == "regexall" && len(inner.Args) == 2 {
				if n, ok := literalNumber(bin.RHS); ok && n == 0 {
					if pattern, ok := constantStringFromExpr(inner.Args[0]); ok {
						return &ExportValidationFolded{Kind: "pattern", Params: map[string]any{"regex": pattern}}
					}
				}
			}
		}
	}
	return nil
}

// regexCallPattern returns the first-arg pattern when expr is
// `regex("<pattern>", ...)`. Used by foldRegex to extract the pattern
// out of the can(...) wrapper.
func regexCallPattern(expr hclsyntax.Expression) (string, bool) {
	call, ok := expr.(*hclsyntax.FunctionCallExpr)
	if !ok || call.Name != "regex" || len(call.Args) < 1 {
		return "", false
	}
	return constantStringFromExpr(call.Args[0])
}

// constantStringFromExpr extracts a constant string literal from expr
// or returns ("", false). hclsyntax wraps bare quoted strings in a
// TemplateExpr containing one LiteralValueExpr — handle both that
// shape and the plain LiteralValueExpr form.
func constantStringFromExpr(expr hclsyntax.Expression) (string, bool) {
	v, diags := expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || !v.IsKnown() {
		return "", false
	}
	if v.Type() != cty.String {
		return "", false
	}
	return v.AsString(), true
}

// ctyValueToInterface converts a cty.Value's known scalar contents
// into a JSON-friendly Go value (string / float64 / bool). Used by
// foldEnum to emit literal enum values directly. Anything more
// complex than a scalar (objects, lists) falls back to the cty/json
// raw form, which keeps the output valid even for unusual enum
// payloads.
func ctyValueToInterface(v cty.Value) any {
	t := v.Type()
	switch {
	case t == cty.String:
		return v.AsString()
	case t == cty.Bool:
		return v.True()
	case t == cty.Number:
		f, _ := v.AsBigFloat().Float64()
		return f
	}
	return v.GoString()
}
