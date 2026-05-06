package render

import (
	"fmt"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/dgr237/tflens/pkg/analysis"
)

// ExportForEachKind classifies a `for_each = ...` expression at export
// time. Saves downstream converters from re-implementing the
// list-vs-map walk against per-RGD type tables, and surfaces a class
// of bugs (where a single value is iterated as if it were a
// collection) at the producer side.
//
// Kinds:
//
//   - "list", "set", "map", "object", "tuple": the inferred type
//   - "scalar": the expression evaluates to a single non-collection
//     value (string/number/bool) — almost always a bug for for_each
//   - "unknown": the analyser couldn't classify the type statically
//   - "invalid": the expression's shape disagrees with what Terraform
//     would treat it as — e.g. the §7 ECS pattern where a ternary's
//     fallback is an empty list but the non-empty branch resolves
//     to a single object. Reason explains; Expected names the kind
//     the fallback shape implies.
//
// Two-tier classification:
//
//  1. **Conditional rule** — when the expression is a ternary with at
//     least one empty `[]` or `{}` literal branch, the empty branch
//     names the *expected* shape (list / object respectively). The
//     non-empty branch is then shape-checked: if it agrees, emit
//     `{kind: expected}`; if a single value is being iterated as a
//     list, emit `{kind: "invalid", reason, expected}`. Falls
//     through to (2) when neither branch is empty.
//
//  2. **Type inference** — for non-conditional expressions, fall back
//     to the analyser's TFType inference (with the index-aware
//     resolver below for traversal expressions whose declared type
//     `Module.InferExprType` truncates).
type ExportForEachKind struct {
	Kind     string `json:"kind"`
	Reason   string `json:"reason,omitempty"`
	Expected string `json:"expected,omitempty"`
}

// classifyForEach inspects a for_each expression and returns its kind.
// Returns nil for nil expressions so omitempty drops the field for
// resources / dynamic blocks / module calls without `for_each`.
//
// Three classifier paths in order:
//
//  1. Conditional rule — for ternary expressions with an empty list /
//     object fallback, applies the empty-branch-anchored
//     expected-vs-actual shape comparison (classifyConditionalForEach).
//  2. Single-value bug — for a non-conditional traversal that resolves
//     through a declared `object({...})` constraint (or a scalar),
//     emits kind: "invalid". Catches the §7 sibling pattern where
//     a for_each source is a single struct rather than a collection
//     (e.g. `for_each = metric_stat.value.metric` where `.metric` is
//     an object field, not a list-of-objects).
//  3. Plain type inference — everything else maps via tfTypeKindString.
//
// When rc is nil callers should not invoke this — every call site
// guarantees a renderCtx.
func classifyForEach(e *analysis.Expr, rc *renderCtx) *ExportForEachKind {
	if e == nil || e.E == nil {
		return nil
	}
	if cond, ok := e.E.(*hclsyntax.ConditionalExpr); ok {
		if k := classifyConditionalForEach(cond, rc); k != nil {
			return k
		}
	}
	t := resolveExprType(e.E, rc)
	if reason, bad := classifySingleValueForEach(e.E, t); bad {
		return &ExportForEachKind{Kind: "invalid", Reason: reason}
	}
	return &ExportForEachKind{Kind: tfTypeKindString(t)}
}

// classifySingleValueForEach detects the §7 sibling bug class:
// a for_each source that resolves to a single value — typically a
// struct-shaped object field declared via `object({...})`, but also
// scalars — instead of a collection. Common shape:
//
//	for_each = metric_stat.value.metric  // .metric is `object(...)`
//
// Terraform iterates the object's attributes (each.key = attr name,
// each.value = attr value) which almost never matches the author's
// intent; the body usually treats each.value as if it were a
// collection element rather than a scalar attribute value.
//
// Recognition requires the source to be a traversal expression (var.X,
// <iter>.value.X, local.X, module.M.O, or any chain of those) AND the
// resolved type to carry a declared cty type (HasCty true) — i.e. not
// inferred from a literal value. This excludes valid in-line literal
// shapes like `for_each = { foo = "bar" }`, where the object-typed
// constructor is the standard way to express a small map.
//
// Returns (reason, true) when the bug is detected; (nil, false) on a
// safe shape. Lower confidence than the conditional rule — only fires
// when both the AST shape (entity reference) and the declared type
// (object / scalar) line up.
func classifySingleValueForEach(expr hclsyntax.Expression, t *analysis.TFType) (string, bool) {
	if t == nil || !t.HasCty() {
		return "", false
	}
	switch expr.(type) {
	case *hclsyntax.ScopeTraversalExpr, *hclsyntax.RelativeTraversalExpr:
	default:
		return "", false
	}
	switch t.Kind {
	case analysis.TypeObject:
		return "for_each source is a single object — Terraform would iterate the object's attributes, not the object itself", true
	case analysis.TypeString, analysis.TypeNumber, analysis.TypeBool:
		return fmt.Sprintf("for_each source is a %s — single value used where a collection is required", scalarTypeName(t.Kind)), true
	}
	return "", false
}

// scalarTypeName returns the wire-format friendly name for a scalar
// TypeKind. Only used in the singleValueForEach reason text — TFType
// has its own String method but TypeKind alone doesn't expose one.
func scalarTypeName(k analysis.TypeKind) string {
	switch k {
	case analysis.TypeString:
		return "string"
	case analysis.TypeNumber:
		return "number"
	case analysis.TypeBool:
		return "bool"
	}
	return "scalar"
}

// classifyConditionalForEach applies the empty-tuple/empty-object rule
// to a ternary for_each expression. Returns nil when neither branch is
// an empty literal — the caller then falls through to plain type
// inference rather than synthesising a bogus classification.
//
// The mismatch case (non-empty branch resolves to scalar or single
// object, fallback is empty list) is the §7 ECS bug class. We name
// the expected kind in the result so converters can report both what
// Terraform will do and what the source presumably intended.
func classifyConditionalForEach(cond *hclsyntax.ConditionalExpr, rc *renderCtx) *ExportForEachKind {
	tEmptyList := isEmptyTuple(cond.TrueResult)
	fEmptyList := isEmptyTuple(cond.FalseResult)
	tEmptyObj := isEmptyObject(cond.TrueResult)
	fEmptyObj := isEmptyObject(cond.FalseResult)

	var expected string
	var nonEmpty hclsyntax.Expression
	switch {
	case tEmptyList && fEmptyList:
		// Both branches empty list — degenerate, just an empty list
		return &ExportForEachKind{Kind: "list"}
	case tEmptyObj && fEmptyObj:
		return &ExportForEachKind{Kind: "object"}
	case tEmptyList || fEmptyList:
		expected = "list"
		nonEmpty = cond.TrueResult
		if tEmptyList {
			nonEmpty = cond.FalseResult
		}
	case tEmptyObj || fEmptyObj:
		expected = "object"
		nonEmpty = cond.TrueResult
		if tEmptyObj {
			nonEmpty = cond.FalseResult
		}
	default:
		return nil
	}

	actual := shapeOf(nonEmpty, rc)
	switch {
	case actual == expected:
		return &ExportForEachKind{Kind: expected}
	case expected == "list" && (actual == "object" || actual == "scalar"):
		return &ExportForEachKind{
			Kind:     "invalid",
			Reason:   fmt.Sprintf("non-empty branch is %s but fallback is empty list — Terraform would iterate %s, not %s", actual, branchIterationDescription(actual), expected),
			Expected: expected,
		}
	case expected == "object" && actual == "scalar":
		return &ExportForEachKind{
			Kind:     "invalid",
			Reason:   "non-empty branch is a scalar but fallback is empty object — Terraform expects a map or object",
			Expected: expected,
		}
	default:
		// actual is "unknown" — refuse to silently coerce to expected.
		// Two failure modes hide here that we don't want to mask:
		//   1. The non-empty branch resolves to a list / map but our
		//      type inference missed it — the consumer would benefit
		//      from at least the fallback hint.
		//   2. The non-empty branch IS a single object/scalar but
		//      hidden behind a passthrough function (try/lookup) or
		//      a local our resolver doesn't trace — emitting
		//      "kind: list" silently would mask the bug.
		//
		// Compromise: emit kind: "unknown" with expected: "<list|object>"
		// so converters can see "this is shaped like a fallback to
		// <expected> but the non-empty branch couldn't be verified".
		// Beats silently labelling buggy code "list".
		return &ExportForEachKind{Kind: "unknown", Expected: expected}
	}
}

// branchIterationDescription names what Terraform would iterate when
// a non-collection value is wrongly placed in a collection-expected
// slot. Used in classifyConditionalForEach's reason field.
func branchIterationDescription(actual string) string {
	switch actual {
	case "object":
		return "the object's attributes as a single-key map"
	case "scalar":
		return "the value as a single-element iteration"
	}
	return "an iteration over the value"
}

// shapeOf returns the broad iteration-classification shape of expr —
// "list" / "object" / "scalar" / "unknown". Used by the conditional
// for_each classifier to compare a non-empty branch against the
// expected shape derived from the empty branch's literal kind.
//
// Resolution strategy:
//
//   - Literal collection / object constructors map directly.
//   - Literal scalars (strings, numbers, bools, templates) → "scalar".
//   - Function calls map via builtinFuncReturns when known.
//   - For-expressions yield list (no key_expr) or object (with key_expr).
//   - Conditional expressions recurse so nested ternaries classify
//     against the same rule.
//   - Traversal expressions (scope or relative) resolve through the
//     index-aware resolver below, which descends declared types via
//     attr/index steps and consults the iterator scope.
//
// "unknown" is returned only when no pathway yields a classification,
// keeping the result space small for the caller's mismatch logic.
func shapeOf(expr hclsyntax.Expression, rc *renderCtx) string {
	switch e := expr.(type) {
	case *hclsyntax.TupleConsExpr:
		return "list"
	case *hclsyntax.ObjectConsExpr:
		return "object"
	case *hclsyntax.LiteralValueExpr:
		t := e.Val.Type()
		switch {
		case t == cty.String, t == cty.Number, t == cty.Bool:
			return "scalar"
		case t.IsListType(), t.IsSetType(), t.IsTupleType():
			return "list"
		case t.IsObjectType(), t.IsMapType():
			return "object"
		}
	case *hclsyntax.TemplateExpr, *hclsyntax.TemplateWrapExpr:
		return "scalar"
	case *hclsyntax.ForExpr:
		if e.KeyExpr != nil {
			return "object"
		}
		return "list"
	case *hclsyntax.FunctionCallExpr:
		if kind, ok := builtinFuncShape[e.Name]; ok {
			return kind
		}
	case *hclsyntax.ConditionalExpr:
		// Recursive case — apply the same conditional rule. If it
		// resolves to a known kind, surface that; otherwise fall
		// through to type inference on the whole conditional.
		if k := classifyConditionalForEach(e, rc); k != nil && k.Kind != "" {
			return canonicalShape(k.Kind)
		}
	case *hclsyntax.ScopeTraversalExpr:
		if t := resolveTraversalType(e.Traversal, rc); t != nil {
			return tfTypeShapeName(t)
		}
	case *hclsyntax.RelativeTraversalExpr:
		if src, ok := e.Source.(*hclsyntax.ScopeTraversalExpr); ok {
			full := append(hcl.Traversal(nil), src.Traversal...)
			full = append(full, e.Traversal...)
			if t := resolveTraversalType(full, rc); t != nil {
				return tfTypeShapeName(t)
			}
		}
	}
	if rc != nil && rc.m != nil {
		if t := rc.m.InferExprType(expr); t != nil && t.Kind != analysis.TypeUnknown {
			return tfTypeShapeName(t)
		}
	}
	return "unknown"
}

// canonicalShape collapses the wider for_each_kind vocabulary into the
// shape vocabulary shapeOf uses (list/object/scalar/unknown). Used
// when a nested ConditionalExpr resolves to a kind that isn't itself
// one of the shape names — e.g. "invalid" or "tuple".
func canonicalShape(kind string) string {
	switch kind {
	case "list", "set", "tuple":
		return "list"
	case "map", "object":
		return "object"
	case "scalar":
		return "scalar"
	}
	return "unknown"
}

// tfTypeShapeName maps a TFType to the shape vocabulary used by
// shapeOf. Mirrors tfTypeKindString but collapses set/tuple into
// "list" and map into "object" so mismatch detection only has three
// concrete buckets to compare.
func tfTypeShapeName(t *analysis.TFType) string {
	if t == nil {
		return "unknown"
	}
	switch t.Kind {
	case analysis.TypeList, analysis.TypeSet, analysis.TypeTuple:
		return "list"
	case analysis.TypeObject, analysis.TypeMap:
		return "object"
	case analysis.TypeString, analysis.TypeNumber, analysis.TypeBool:
		return "scalar"
	}
	return "unknown"
}

// builtinFuncShape maps Terraform built-in function names to their
// for_each shape. A trimmed mirror of the analyser's
// builtinFuncReturns: only functions that produce a clearly-shaped
// result are included, so an unknown function falls through to
// "unknown" rather than being given a shape it doesn't actually have.
var builtinFuncShape = map[string]string{
	"toset":    "list",
	"tomap":    "object",
	"tolist":   "list",
	"tostring": "scalar",
	"tonumber": "scalar",
	"tobool":   "scalar",

	"concat":    "list",
	"flatten":   "list",
	"reverse":   "list",
	"sort":      "list",
	"compact":   "list",
	"distinct":  "list",
	"keys":      "list",
	"values":    "list",
	"slice":     "list",
	"chunklist": "list",
	"range":     "list",
	"split":     "list",

	"merge":  "object",
	"zipmap": "object",

	"length":    "scalar",
	"max":       "scalar",
	"min":       "scalar",
	"format":    "scalar",
	"join":      "scalar",
	"lower":     "scalar",
	"upper":     "scalar",
	"trimspace": "scalar",
	"replace":   "scalar",
	"contains":  "scalar",
	"can":       "scalar",
	"alltrue":   "scalar",
	"anytrue":   "scalar",

	"jsonencode":   "scalar",
	"yamlencode":   "scalar",
	"base64encode": "scalar",
	"jsondecode":   "unknown",
	"yamldecode":   "unknown",
	"file":         "scalar",
	"templatefile": "scalar",
}

// resolveExprType is the dispatcher that classifyForEach's fallback
// path uses for non-conditional expressions. Prefers the index-aware
// resolveTraversalType for traversal expressions (the analyser's
// stock InferExprType truncates traversals at the first non-attr
// step, which loses information for shapes like
// `var.instances["primary"].metric_stat`); peers through passthrough
// function wrappers (try/lookup/coalesce) so common defensive idioms
// don't break inference; falls back to the analyser's inference for
// everything else.
func resolveExprType(expr hclsyntax.Expression, rc *renderCtx) *analysis.TFType {
	if inner, ok := unwrapPassthrough(expr); ok {
		if t := resolveExprType(inner, rc); t != nil {
			return t
		}
	}
	switch e := expr.(type) {
	case *hclsyntax.ScopeTraversalExpr:
		if t := resolveTraversalType(e.Traversal, rc); t != nil {
			return t
		}
	case *hclsyntax.RelativeTraversalExpr:
		if src, ok := e.Source.(*hclsyntax.ScopeTraversalExpr); ok {
			full := append(hcl.Traversal(nil), src.Traversal...)
			full = append(full, e.Traversal...)
			if t := resolveTraversalType(full, rc); t != nil {
				return t
			}
		}
	}
	if rc != nil && rc.m != nil {
		if t := rc.m.InferExprType(expr); t != nil && t.Kind != analysis.TypeUnknown {
			return t
		}
	}
	return nil
}

// unwrapPassthrough recognises Terraform idioms where an expression
// is wrapped in a "best-effort" function call that passes the first
// argument's type through:
//
//   - try(X, ...)           — first arg's type if X is a known shape
//   - lookup(_, _, default) — third arg (the default) is the only
//     statically-known shape; we use it when present
//   - coalesce(X, ...)      — first arg's type
//
// Returns the inner expression and true when a known passthrough
// wrapper is recognised. Used by resolveExprType so chains like
// `try(metric_data_query.value.metric_stat, null)` don't lose the
// type that the inner traversal would have resolved.
func unwrapPassthrough(expr hclsyntax.Expression) (hclsyntax.Expression, bool) {
	call, ok := expr.(*hclsyntax.FunctionCallExpr)
	if !ok || len(call.Args) == 0 {
		return nil, false
	}
	switch call.Name {
	case "try", "coalesce":
		return call.Args[0], true
	case "lookup":
		if len(call.Args) >= 3 {
			return call.Args[2], true
		}
	}
	return nil, false
}

// tfTypeKindString maps a TFType to the wire-format kind string. Used
// directly by classifyForEach for non-conditional expressions where
// every TFType.Kind is a valid for_each_kind value (the "invalid"
// kind is reserved for the conditional rule's mismatch case).
func tfTypeKindString(t *analysis.TFType) string {
	if t == nil {
		return "unknown"
	}
	switch t.Kind {
	case analysis.TypeList:
		return "list"
	case analysis.TypeSet:
		return "set"
	case analysis.TypeMap:
		return "map"
	case analysis.TypeObject:
		return "object"
	case analysis.TypeTuple:
		return "tuple"
	case analysis.TypeString, analysis.TypeNumber, analysis.TypeBool:
		return "scalar"
	}
	return "unknown"
}

// isEmptyTuple reports whether expr is the literal `[]`. Used by the
// conditional classifier to recognise the fallback branch.
func isEmptyTuple(expr hclsyntax.Expression) bool {
	t, ok := expr.(*hclsyntax.TupleConsExpr)
	return ok && len(t.Exprs) == 0
}

// isEmptyObject reports whether expr is the literal `{}`. Used by the
// conditional classifier to recognise the fallback branch when the
// expected shape is object/map.
func isEmptyObject(expr hclsyntax.Expression) bool {
	o, ok := expr.(*hclsyntax.ObjectConsExpr)
	return ok && len(o.Items) == 0
}

// iteratorElementType infers the per-iteration element type of a
// for_each source expression — used by exportResource and
// exportDynamicBlock to push the iterator's value-type binding onto
// the renderCtx before descending into content. Returns nil when no
// element type can be inferred, in which case pushIterator no-ops and
// `<iter>.value(.path)` lookups inside the content body fall through
// to "unknown" via the resolver.
//
// Handles three structural shapes the analyser's stock type inference
// would otherwise truncate or lose:
//
//   - Conditional with an empty fallback (`cond ? X : []`): the
//     element type comes from X, recursively. This is the common
//     idiom for safely iterating an optional collection / object
//     and is the chain that breaks downstream resolution when not
//     unwrapped here.
//   - Conditional with a singleton-as-list (`cond ? [X] : []`): the
//     element type is X's type. Same correct pattern §7 of the bug
//     class points to as the safe fix; we want the iteration chain
//     to keep flowing through it.
//   - Tuple constructors (`[X]` / `[X, Y, Z]`): element type is X's
//     type when all elements share a shape, or unknown otherwise.
//
// Then falls through to the standard list/set/map → Elem extraction
// for plain traversals and other non-conditional sources.
func iteratorElementType(expr hclsyntax.Expression, rc *renderCtx) *analysis.TFType {
	switch e := expr.(type) {
	case *hclsyntax.ConditionalExpr:
		// Pick the non-empty branch — the iteration "really" comes
		// from there. If both branches are empty the result is empty
		// anyway and the binding doesn't help.
		var nonEmpty hclsyntax.Expression
		switch {
		case isEmptyTuple(e.TrueResult):
			nonEmpty = e.FalseResult
		case isEmptyTuple(e.FalseResult):
			nonEmpty = e.TrueResult
		case isEmptyObject(e.TrueResult):
			nonEmpty = e.FalseResult
		case isEmptyObject(e.FalseResult):
			nonEmpty = e.TrueResult
		}
		if nonEmpty != nil {
			return iteratorElementType(nonEmpty, rc)
		}
	case *hclsyntax.TupleConsExpr:
		if len(e.Exprs) == 0 {
			return nil
		}
		return resolveExprType(e.Exprs[0], rc)
	case *hclsyntax.ForExpr:
		// Common idiom: `{ for k, v in S : k => v if cond }` —
		// passthrough for-expression that filters / re-keys S without
		// changing the element type. When the value expression is a
		// bare reference to the iteration value-var, the result's
		// element type equals S's element type. Anything more
		// elaborate (transformation of v, computed objects) falls
		// through to nil — too easy to produce a bogus binding.
		srcElem := iteratorElementType(e.CollExpr, rc)
		if srcElem == nil {
			return nil
		}
		if stv, ok := e.ValExpr.(*hclsyntax.ScopeTraversalExpr); ok && len(stv.Traversal) == 1 {
			if root, ok := stv.Traversal[0].(hcl.TraverseRoot); ok && root.Name == e.ValVar {
				return srcElem
			}
		}
		return nil
	}
	t := resolveExprType(expr, rc)
	if t == nil {
		return nil
	}
	switch t.Kind {
	case analysis.TypeList, analysis.TypeSet, analysis.TypeMap:
		return t.Elem
	case analysis.TypeObject:
		// Bug-pattern accommodation: push the object itself as the
		// iterator's synthetic element type. Terraform's actual
		// semantics for `for_each = <object>` is to iterate
		// attribute key/value pairs (each.key = attr name,
		// each.value = the attribute's value), so this binding is
		// "wrong" relative to runtime — but the for_each itself is
		// already flagged invalid by classifySingleValueForEach in
		// those cases, and pushing the object lets downstream
		// `<iter>.value.X` resolve into the declared object's
		// fields so cascading single-object bugs surface their own
		// invalid classifications instead of cascading to unknown.
		// Literal `{...}` constructors have no Fields populated so
		// downstream resolution naturally falls back to unknown for
		// legitimate object-as-map for_each idioms.
		return t
	}
	return nil
}

// resolveTraversalType walks a traversal step-by-step into the
// declared type that the traversal's root names — `var.X`, an
// iterator binding from rc.iterators, or `module.M.O`. Descends
// through both attribute and index steps so shapes like
// `var.instances["primary"].metric_stat` resolve to the field type
// rather than truncating at the first non-attr step (which the
// analyser's stock inferValueType does).
//
// Roots resolved:
//
//   - `var.<name>(...)` — through the variable's declared type.
//   - `<iter>.value(...)` — through the matching iterator's element
//     type, with the rc.iterators stack walked innermost-first so
//     a shadowing inner iterator wins.
//   - `<iter>.key` — always cty.String for map iteration.
//   - `module.<call>.<output>(...)` — through the called child
//     module's output value expression's inferred type, when the
//     child is loaded via rc.children.
//
// Returns nil for unsupported roots and for any step that doesn't
// apply to the current type. Caller treats nil as "unknown" — the
// conservative interpretation when we can't statically resolve.
func resolveTraversalType(trav hcl.Traversal, rc *renderCtx) *analysis.TFType {
	if len(trav) < 1 {
		return nil
	}
	root, ok := trav[0].(hcl.TraverseRoot)
	if !ok {
		return nil
	}
	switch root.Name {
	case "var":
		return resolveVarTraversal(trav, rc)
	case "local":
		return resolveLocalTraversal(trav, rc)
	case "module":
		return resolveModuleTraversal(trav, rc)
	}
	if t := resolveIteratorTraversal(root.Name, trav, rc); t != nil {
		return t
	}
	return nil
}

// resolveLocalTraversal handles `local.<name>(.path...)`. Locals
// don't carry declared type constraints in Terraform, so we infer
// the local's type from its value expression — recursively, since
// locals can reference other locals / vars / iterators / module
// outputs. Returns nil when the local isn't declared, its value
// expression resolves to unknown, or the resolution chain hits a
// cycle (`local.A → local.B → local.A`).
//
// Cycle protection: rc.resolvingLocals tracks the set of local names
// currently mid-resolution. Re-entering one short-circuits to nil so
// the caller treats the local as "type unknown" rather than recursing
// to stack overflow. Terraform itself rejects local cycles at validate
// time, so this only matters for in-development / partially-broken
// configs that the export still has to terminate on.
func resolveLocalTraversal(trav hcl.Traversal, rc *renderCtx) *analysis.TFType {
	if rc == nil || rc.m == nil || len(trav) < 2 {
		return nil
	}
	nameStep, ok := trav[1].(hcl.TraverseAttr)
	if !ok {
		return nil
	}
	if rc.resolvingLocals[nameStep.Name] {
		return nil
	}
	l, ok := rc.m.EntityByID((analysis.Entity{Kind: analysis.KindLocal, Name: nameStep.Name}).ID())
	if !ok || l.LocalExpr == nil || l.LocalExpr.E == nil {
		return nil
	}
	if rc.resolvingLocals == nil {
		rc.resolvingLocals = map[string]bool{}
	}
	rc.resolvingLocals[nameStep.Name] = true
	defer delete(rc.resolvingLocals, nameStep.Name)
	t := resolveExprType(l.LocalExpr.E, rc)
	return descendType(t, trav[2:])
}

// resolveVarTraversal handles `var.<name>(.path...)` — looks up the
// variable's declared type in rc.m and descends through the remaining
// steps via descendType.
func resolveVarTraversal(trav hcl.Traversal, rc *renderCtx) *analysis.TFType {
	if rc == nil || rc.m == nil || len(trav) < 2 {
		return nil
	}
	nameStep, ok := trav[1].(hcl.TraverseAttr)
	if !ok {
		return nil
	}
	v, ok := rc.m.EntityByID((analysis.Entity{Kind: analysis.KindVariable, Name: nameStep.Name}).ID())
	if !ok || v.DeclaredType == nil {
		return nil
	}
	return descendType(v.DeclaredType, trav[2:])
}

// resolveIteratorTraversal handles `<iter>.value(.path...)` and
// `<iter>.key`. Walks rc.iterators innermost-first so a shadowing
// inner iterator wins over an outer one of the same name.
//
// `<iter>.value` returns the iterator's element type (descended into
// any further path steps). `<iter>.key` returns string — Terraform's
// iteration key is always a string for map sources.
func resolveIteratorTraversal(name string, trav hcl.Traversal, rc *renderCtx) *analysis.TFType {
	if rc == nil || len(rc.iterators) == 0 || len(trav) < 2 {
		return nil
	}
	for i := len(rc.iterators) - 1; i >= 0; i-- {
		scope := rc.iterators[i]
		if scope.name != name {
			continue
		}
		attr, ok := trav[1].(hcl.TraverseAttr)
		if !ok {
			return scope.elementType
		}
		switch attr.Name {
		case "value":
			return descendType(scope.elementType, trav[2:])
		case "key":
			return &analysis.TFType{Kind: analysis.TypeString}
		}
		return nil
	}
	return nil
}

// resolveModuleTraversal handles `module.<call>.<output>(.path...)`.
// Looks up the called child module via rc.children and infers the
// type of the output's value expression in the child's own module
// context (so var.X references inside the output value resolve through
// the child's variables, not the parent's).
//
// Most outputs reference computed resource attributes whose types
// the analyser can't infer, so this typically returns nil. Outputs
// that pass a variable through (`output "x" { value = var.x }`) do
// resolve cleanly though, which is the case `<iter>.value` patterns
// running through a sub-module rely on.
func resolveModuleTraversal(trav hcl.Traversal, rc *renderCtx) *analysis.TFType {
	if rc == nil || rc.children == nil || len(trav) < 3 {
		return nil
	}
	callStep, ok := trav[1].(hcl.TraverseAttr)
	if !ok {
		return nil
	}
	outStep, ok := trav[2].(hcl.TraverseAttr)
	if !ok {
		return nil
	}
	child, ok := rc.children[callStep.Name]
	if !ok || child == nil || child.Module == nil {
		return nil
	}
	out, ok := child.Module.EntityByID((analysis.Entity{Kind: analysis.KindOutput, Name: outStep.Name}).ID())
	if !ok || out.ValueExpr == nil || out.ValueExpr.E == nil {
		return nil
	}
	t := child.Module.InferExprType(out.ValueExpr.E)
	if t == nil || t.Kind == analysis.TypeUnknown {
		return nil
	}
	return descendType(t, trav[3:])
}

// descendType walks the post-prefix steps of a traversal into a
// declared type, descending through attribute steps (object fields,
// map elements) and index steps (list/map/set elements, tuple
// positions, object fields by string-literal key). Returns nil when
// any step doesn't apply to the current type — the caller treats nil
// as "unknown".
func descendType(cur *analysis.TFType, steps []hcl.Traverser) *analysis.TFType {
	for _, step := range steps {
		if cur == nil {
			return nil
		}
		switch s := step.(type) {
		case hcl.TraverseAttr:
			switch cur.Kind {
			case analysis.TypeObject:
				cur = cur.Fields[s.Name]
			case analysis.TypeMap:
				cur = cur.Elem
			default:
				return nil
			}
		case hcl.TraverseIndex:
			switch cur.Kind {
			case analysis.TypeMap, analysis.TypeList, analysis.TypeSet:
				cur = cur.Elem
			case analysis.TypeObject:
				if s.Key.Type() == cty.String && !s.Key.IsNull() && s.Key.IsKnown() {
					cur = cur.Fields[s.Key.AsString()]
				} else {
					return nil
				}
			case analysis.TypeTuple:
				if s.Key.Type() == cty.Number && !s.Key.IsNull() && s.Key.IsKnown() {
					n, _ := s.Key.AsBigFloat().Int64()
					if n >= 0 && int(n) < len(cur.Elems) {
						cur = cur.Elems[n]
					} else {
						return nil
					}
				} else {
					return nil
				}
			default:
				return nil
			}
		default:
			return nil
		}
	}
	return cur
}
