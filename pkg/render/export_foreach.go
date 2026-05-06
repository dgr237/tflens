package render

import (
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/dgr237/tflens/pkg/analysis"
)

// ExportForEachKind is the wire-format projection of
// analysis.ForEachClassification. Same fields, json-tagged for the
// export envelope. Kept in render rather than analysis because
// analysis intentionally has no JSON-shape opinions — its job is
// classification, not serialisation.
type ExportForEachKind struct {
	Kind     string `json:"kind"`
	Reason   string `json:"reason,omitempty"`
	Expected string `json:"expected,omitempty"`
}

// classifyForEach is a thin wire-format adapter over
// analysis.Resolver.ClassifyForEach. Returns nil for nil input so the
// `omitempty` tag drops the for_each_kind field on resources / dynamic
// blocks / module calls without `for_each`.
func classifyForEach(e *analysis.Expr, rc *renderCtx) *ExportForEachKind {
	if rc == nil || rc.resolver == nil {
		return nil
	}
	c := rc.resolver.ClassifyForEach(e)
	if c == nil {
		return nil
	}
	return &ExportForEachKind{
		Kind:     c.Kind,
		Reason:   c.Reason,
		Expected: c.Expected,
	}
}

// iteratorElementType delegates to the analysis resolver. Render
// callers (exportResource, exportDynamicBlock) keep using a
// render-package name for readability while the implementation lives
// down in analysis alongside the rest of the resolver.
func iteratorElementType(expr hclsyntax.Expression, rc *renderCtx) *analysis.TFType {
	if rc == nil || rc.resolver == nil {
		return nil
	}
	return rc.resolver.IteratorElementType(expr)
}

// resolveExprType delegates to the analysis resolver. Used by
// exportLocal / exportOutput to populate inferred_type fields.
func resolveExprType(expr hclsyntax.Expression, rc *renderCtx) *analysis.TFType {
	if rc == nil || rc.resolver == nil {
		return nil
	}
	return rc.resolver.ResolveExprType(expr)
}

// isEmptyTuple / isEmptyObject thin re-exports for callers in render
// (export_ast.go's conditionalPattern uses them). Kept as render-side
// names so the AST pattern code reads cleanly without an analysis
// prefix on every call.
func isEmptyTuple(expr hclsyntax.Expression) bool {
	return analysis.IsEmptyTupleLit(expr)
}

func isEmptyObject(expr hclsyntax.Expression) bool {
	return analysis.IsEmptyObjectLit(expr)
}
