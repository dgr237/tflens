package analysis

import (
	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// Resolver provides type inference + for_each classification on top of
// a Module. Holds three pieces of per-call state that the existing
// Module-level inferValueType doesn't:
//
//   - iterators: a stack of dynamic-block iterator bindings (`<name>.value`
//     resolves through the binding's element type), pushed via PushIterator.
//   - resolvingLocals: cycle protection for transitive local-value type
//     inference (resolveLocalTraversal recurses into local.X.value's
//     expression; without this set, A → B → A loops to stack overflow).
//   - childModuleFn: optional getter for a module call's analysed child,
//     so `module.M.O.path` resolves through the child's output value
//     expression. Render passes a loader-backed closure; analysis-internal
//     callers (typecheck) leave it nil.
//
// Methods on Resolver are the index-aware, iterator-scope-aware,
// passthrough-aware counterpart to the analyser's existing
// inferValueType — strictly more capable. The classifier methods
// (ClassifyForEach) implement the §7 conditional rule.
type Resolver struct {
	m               *Module
	iterators       []IteratorScope
	resolvingLocals map[string]bool
	childModuleFn   func(string) *Module
}

// IteratorScope is one dynamic-block iterator binding: the iterator
// name (defaulting to the dynamic block's label when not explicitly
// renamed via `iterator =`) and the element type of the for_each
// source. Walked innermost-first by ResolveTraversalType so a
// shadowing inner iterator wins over an outer one of the same name.
type IteratorScope struct {
	Name        string
	ElementType *TFType
}

// Resolver returns a new Resolver rooted at this module. Returns nil
// for a nil receiver. The returned Resolver has no iterator bindings
// and no childModule getter — call WithChildModuleGetter and
// PushIterator to extend.
func (m *Module) Resolver() *Resolver {
	if m == nil {
		return nil
	}
	return &Resolver{m: m, resolvingLocals: map[string]bool{}}
}

// WithChildModuleGetter returns a copy of r with the given child
// module getter installed. The getter is consulted by
// ResolveTraversalType when it sees a `module.<call>.<output>(.path...)`
// reference; returning nil indicates the child isn't available
// (e.g. unresolved git/registry source) and the resolver short-circuits.
func (r *Resolver) WithChildModuleGetter(f func(string) *Module) *Resolver {
	if r == nil {
		return nil
	}
	out := *r
	out.childModuleFn = f
	return &out
}

// PushIterator returns a new Resolver whose iterator stack ends with
// the given binding. Returns r unchanged when scope.Name is empty
// (malformed dynamic block) or scope.ElementType is nil (we couldn't
// infer the for_each source's element type, in which case the binding
// wouldn't help downstream resolution anyway).
//
// The new stack is allocated independently so siblings don't share a
// growing append-target slice — important because export's recursive
// walks descend into multiple branches that each need their own scope
// view. The resolvingLocals map is shared (Go map = reference) so
// cycle protection survives iterator pushes mid-resolution.
func (r *Resolver) PushIterator(scope IteratorScope) *Resolver {
	if r == nil || scope.Name == "" || scope.ElementType == nil {
		return r
	}
	stack := make([]IteratorScope, len(r.iterators)+1)
	copy(stack, r.iterators)
	stack[len(r.iterators)] = scope
	out := *r
	out.iterators = stack
	return &out
}

// Module returns the module this resolver is rooted at. Convenience for
// callers that need both the resolver and the underlying module.
func (r *Resolver) Module() *Module {
	if r == nil {
		return nil
	}
	return r.m
}

// ---- type inference ----

// ResolveExprType is the dispatcher: given any expression, returns the
// best-effort statically-resolved type. Five resolution strategies in
// order:
//
//  1. Passthrough function unwrap (try / lookup / coalesce) — recover
//     the wrapped expression's type so common defensive idioms don't
//     lose inference.
//  2. ConditionalExpr — peer through ternaries with an empty `[]` or
//     `{}` fallback to the non-empty branch (the common defensive-
//     default idiom for optional collections), and pick a branch when
//     both type-agree (the `cond ? var.x : null` shape).
//  3. ScopeTraversalExpr — index-aware ResolveTraversalType. The
//     analyser's stock InferExprType truncates traversals at the
//     first non-attr step, which loses information for shapes like
//     `var.instances["primary"].metric_stat`.
//  4. RelativeTraversalExpr with ScopeTraversal source — same as (3)
//     after concatenating the source's traversal with the relative
//     chain.
//  5. Module's stock InferExprType — fallback for everything else.
//
// Returns nil when no path yields a known type. Caller treats nil as
// "unknown" — the conservative interpretation.
func (r *Resolver) ResolveExprType(expr hclsyntax.Expression) *TFType {
	if r == nil || expr == nil {
		return nil
	}
	if inner, ok := unwrapPassthrough(expr); ok {
		if t := r.ResolveExprType(inner); t != nil {
			return t
		}
	}
	if cond, ok := expr.(*hclsyntax.ConditionalExpr); ok {
		if t := r.resolveConditionalType(cond); t != nil {
			return t
		}
	}
	switch e := expr.(type) {
	case *hclsyntax.ScopeTraversalExpr:
		if t := r.ResolveTraversalType(e.Traversal); t != nil {
			return t
		}
	case *hclsyntax.RelativeTraversalExpr:
		if src, ok := e.Source.(*hclsyntax.ScopeTraversalExpr); ok {
			full := append(hcl.Traversal(nil), src.Traversal...)
			full = append(full, e.Traversal...)
			if t := r.ResolveTraversalType(full); t != nil {
				return t
			}
		}
	}
	if r.m != nil {
		if t := r.m.InferExprType(expr); t != nil && t.Kind != TypeUnknown {
			return t
		}
	}
	return nil
}

// ResolveTraversalType walks a traversal step-by-step into the declared
// type that the traversal's root names. Roots resolved:
//
//   - `var.<name>(...)` — through the variable's declared type.
//   - `<iter>.value(...)` — through the matching iterator scope's
//     element type, walked innermost-first so a shadowing inner
//     iterator wins.
//   - `<iter>.key` — always cty.String for map iteration.
//   - `module.<call>.<output>(...)` — through the called child
//     module's output value expression's inferred type, when
//     childModuleFn is installed and returns the child.
//   - `local.<name>(...)` — through the local's value expression
//     (recursive, with cycle protection via resolvingLocals).
//
// Returns nil for unsupported roots and any step that doesn't apply
// to the current type. Caller treats nil as "unknown".
func (r *Resolver) ResolveTraversalType(trav hcl.Traversal) *TFType {
	if r == nil || len(trav) < 1 {
		return nil
	}
	root, ok := trav[0].(hcl.TraverseRoot)
	if !ok {
		return nil
	}
	switch root.Name {
	case "var":
		return r.resolveVarTraversal(trav)
	case "local":
		return r.resolveLocalTraversal(trav)
	case "module":
		return r.resolveModuleTraversal(trav)
	}
	if t := r.resolveIteratorTraversal(root.Name, trav); t != nil {
		return t
	}
	return nil
}

func (r *Resolver) resolveVarTraversal(trav hcl.Traversal) *TFType {
	if r.m == nil || len(trav) < 2 {
		return nil
	}
	nameStep, ok := trav[1].(hcl.TraverseAttr)
	if !ok {
		return nil
	}
	v, ok := r.m.EntityByID((Entity{Kind: KindVariable, Name: nameStep.Name}).ID())
	if !ok || v.DeclaredType == nil {
		return nil
	}
	return descendType(v.DeclaredType, trav[2:])
}

// resolveIteratorTraversal handles `<iter>.value(.path...)` and
// `<iter>.key`. Walks r.iterators innermost-first so a shadowing inner
// iterator wins over an outer one of the same name.
func (r *Resolver) resolveIteratorTraversal(name string, trav hcl.Traversal) *TFType {
	if len(r.iterators) == 0 || len(trav) < 2 {
		return nil
	}
	for i := len(r.iterators) - 1; i >= 0; i-- {
		scope := r.iterators[i]
		if scope.Name != name {
			continue
		}
		attr, ok := trav[1].(hcl.TraverseAttr)
		if !ok {
			return scope.ElementType
		}
		switch attr.Name {
		case "value":
			return descendType(scope.ElementType, trav[2:])
		case "key":
			return &TFType{Kind: TypeString}
		}
		return nil
	}
	return nil
}

// resolveModuleTraversal handles `module.<call>.<output>(.path...)`.
// Looks up the called child module via the installed getter and
// infers the type of the output's value expression in the child's
// own module context (so var.X references inside the output value
// resolve through the child's variables, not the parent's).
//
// Most outputs reference computed resource attributes whose types
// the analyser can't infer, so this typically returns nil. Outputs
// that pass a variable through (`output "x" { value = var.x }`) do
// resolve cleanly though.
func (r *Resolver) resolveModuleTraversal(trav hcl.Traversal) *TFType {
	if r.childModuleFn == nil || len(trav) < 3 {
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
	child := r.childModuleFn(callStep.Name)
	if child == nil {
		return nil
	}
	out, ok := child.EntityByID((Entity{Kind: KindOutput, Name: outStep.Name}).ID())
	if !ok || out.ValueExpr == nil || out.ValueExpr.E == nil {
		return nil
	}
	t := child.InferExprType(out.ValueExpr.E)
	if t == nil || t.Kind == TypeUnknown {
		return nil
	}
	return descendType(t, trav[3:])
}

// resolveLocalTraversal handles `local.<name>(.path...)`. Locals don't
// carry declared type constraints in Terraform, so we infer the local's
// type from its value expression — recursively, since locals can
// reference other locals / vars / iterators / module outputs.
//
// Cycle protection: r.resolvingLocals tracks the set of local names
// currently mid-resolution. Re-entering one short-circuits to nil so
// the caller treats the local as "type unknown" rather than recursing
// to stack overflow. Terraform itself rejects local cycles at validate
// time, so this only matters for in-development / partially-broken
// configs that the export still has to terminate on.
func (r *Resolver) resolveLocalTraversal(trav hcl.Traversal) *TFType {
	if r.m == nil || len(trav) < 2 {
		return nil
	}
	nameStep, ok := trav[1].(hcl.TraverseAttr)
	if !ok {
		return nil
	}
	if r.resolvingLocals[nameStep.Name] {
		return nil
	}
	l, ok := r.m.EntityByID((Entity{Kind: KindLocal, Name: nameStep.Name}).ID())
	if !ok || l.LocalExpr == nil || l.LocalExpr.E == nil {
		return nil
	}
	if r.resolvingLocals == nil {
		r.resolvingLocals = map[string]bool{}
	}
	r.resolvingLocals[nameStep.Name] = true
	defer delete(r.resolvingLocals, nameStep.Name)
	t := r.ResolveExprType(l.LocalExpr.E)
	return descendType(t, trav[2:])
}

// resolveConditionalType infers the result type of a ternary by
// picking the more informative branch. Strategy:
//
//   - One branch is `[]` or `{}` (empty literal) — return the non-empty
//     branch's type. The fallback's empty-literal type carries less
//     information.
//   - One branch is null literal — return the non-null branch's type.
//     `cond ? var.x : null` is the common nullable-passthrough idiom.
//   - Both branches resolve and types agree — return that type.
//   - Otherwise — return whichever branch resolved, or nil.
func (r *Resolver) resolveConditionalType(cond *hclsyntax.ConditionalExpr) *TFType {
	tEmpty := IsEmptyTupleLit(cond.TrueResult) || IsEmptyObjectLit(cond.TrueResult)
	fEmpty := IsEmptyTupleLit(cond.FalseResult) || IsEmptyObjectLit(cond.FalseResult)
	if tEmpty && !fEmpty {
		return r.ResolveExprType(cond.FalseResult)
	}
	if fEmpty && !tEmpty {
		return r.ResolveExprType(cond.TrueResult)
	}
	tNull := IsNullLit(cond.TrueResult)
	fNull := IsNullLit(cond.FalseResult)
	if tNull && !fNull {
		return r.ResolveExprType(cond.FalseResult)
	}
	if fNull && !tNull {
		return r.ResolveExprType(cond.TrueResult)
	}
	tType := r.ResolveExprType(cond.TrueResult)
	fType := r.ResolveExprType(cond.FalseResult)
	if tType != nil && fType != nil && tType.Kind == fType.Kind {
		return tType
	}
	if tType != nil {
		return tType
	}
	return fType
}

// IteratorElementType infers the per-iteration element type of a
// for_each source expression — used by the export and the type-check
// pass to push the iterator's value-type binding before descending into
// content. Returns nil when no element type can be inferred.
//
// Handles the same structural shapes as ResolveExprType plus three
// iteration-specific patterns:
//
//   - Conditional with an empty fallback (`cond ? X : []`): element
//     type comes from X, recursively. Common idiom for iterating
//     an optional collection.
//   - Conditional with a singleton-as-list (`cond ? [X] : []`):
//     element type is X's type. Same correct pattern §7 points to.
//   - Tuple constructor (`[X]` / `[X, Y, Z]`): element type is X's
//     type when all elements share a shape (heuristic: take the first).
//   - For-expression (`{for k, v in S : k => v if cond}`): element
//     type is S's element type when the value expression is a bare
//     reference to the iteration value-var.
//
// Then falls through to the standard list/set/map → Elem extraction
// for plain traversals. For TypeObject sources, returns the object
// itself as the synthetic element type — Terraform's actual semantics
// for object for_each iterate attribute key/value pairs (so each.value
// would be the attribute value, not the object), but this binding
// lets cascading single-object bugs surface their own invalid
// classifications instead of falling through to unknown. Literal
// `{...}` constructors have empty Fields so legitimate object-as-map
// idioms still cascade harmlessly to unknown.
func (r *Resolver) IteratorElementType(expr hclsyntax.Expression) *TFType {
	if r == nil || expr == nil {
		return nil
	}
	switch e := expr.(type) {
	case *hclsyntax.ConditionalExpr:
		var nonEmpty hclsyntax.Expression
		switch {
		case IsEmptyTupleLit(e.TrueResult):
			nonEmpty = e.FalseResult
		case IsEmptyTupleLit(e.FalseResult):
			nonEmpty = e.TrueResult
		case IsEmptyObjectLit(e.TrueResult):
			nonEmpty = e.FalseResult
		case IsEmptyObjectLit(e.FalseResult):
			nonEmpty = e.TrueResult
		}
		if nonEmpty != nil {
			return r.IteratorElementType(nonEmpty)
		}
	case *hclsyntax.TupleConsExpr:
		if len(e.Exprs) == 0 {
			return nil
		}
		return r.ResolveExprType(e.Exprs[0])
	case *hclsyntax.ForExpr:
		srcElem := r.IteratorElementType(e.CollExpr)
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
	t := r.ResolveExprType(expr)
	if t == nil {
		return nil
	}
	switch t.Kind {
	case TypeList, TypeSet, TypeMap:
		return t.Elem
	case TypeObject:
		return t
	}
	return nil
}

// ---- for_each classification ----

// ForEachClassification is the named-shape result of ClassifyForEach.
// Kind names the inferred shape (list / map / set / object / tuple /
// scalar / unknown) or "invalid" when the rule detects a mismatch
// (the §7 bug class). Reason explains invalid kinds; Expected names
// the kind that the source's structure (typically the empty-fallback
// branch) implied was intended.
type ForEachClassification struct {
	Kind     string
	Reason   string
	Expected string
}

// ClassifyForEach inspects a for_each expression and returns its
// classification. Three classifier paths in order:
//
//  1. Conditional rule — for ternary expressions with an empty list /
//     object fallback, applies the empty-branch-anchored expected-vs-
//     actual shape comparison.
//  2. Single-value bug — for a non-conditional traversal that resolves
//     through a declared `object({...})` constraint (or a scalar),
//     emits Kind: "invalid". Catches the §7 sibling pattern where
//     a for_each source is a single struct rather than a collection.
//  3. Plain type inference — everything else maps via the type kind
//     name from ResolveExprType.
//
// Returns nil for nil expressions so callers can plug it directly
// into pointer-typed result fields.
func (r *Resolver) ClassifyForEach(e *Expr) *ForEachClassification {
	if e == nil || e.E == nil || r == nil {
		return nil
	}
	if cond, ok := e.E.(*hclsyntax.ConditionalExpr); ok {
		if k := r.classifyConditionalForEach(cond); k != nil {
			return k
		}
	}
	t := r.ResolveExprType(e.E)
	if reason, bad := classifySingleValueForEach(e.E, t); bad {
		return &ForEachClassification{Kind: "invalid", Reason: reason}
	}
	return &ForEachClassification{Kind: tfTypeKindName(t)}
}

// classifyConditionalForEach applies the empty-tuple/empty-object rule
// to a ternary for_each expression. Returns nil when neither branch is
// an empty literal — the caller then falls through to plain type
// inference rather than synthesising a bogus classification.
func (r *Resolver) classifyConditionalForEach(cond *hclsyntax.ConditionalExpr) *ForEachClassification {
	tEmptyList := IsEmptyTupleLit(cond.TrueResult)
	fEmptyList := IsEmptyTupleLit(cond.FalseResult)
	tEmptyObj := IsEmptyObjectLit(cond.TrueResult)
	fEmptyObj := IsEmptyObjectLit(cond.FalseResult)

	var expected string
	var nonEmpty hclsyntax.Expression
	switch {
	case tEmptyList && fEmptyList:
		return &ForEachClassification{Kind: "list"}
	case tEmptyObj && fEmptyObj:
		return &ForEachClassification{Kind: "object"}
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

	actual := r.shapeOf(nonEmpty)
	switch {
	case actual == expected:
		return &ForEachClassification{Kind: expected}
	case expected == "list" && (actual == "object" || actual == "scalar"):
		return &ForEachClassification{
			Kind:     "invalid",
			Reason:   "non-empty branch is " + actual + " but fallback is empty list — Terraform would iterate " + branchIterDescription(actual) + ", not " + expected,
			Expected: expected,
		}
	case expected == "object" && actual == "scalar":
		return &ForEachClassification{
			Kind:     "invalid",
			Reason:   "non-empty branch is a scalar but fallback is empty object — Terraform expects a map or object",
			Expected: expected,
		}
	default:
		return &ForEachClassification{Kind: "unknown", Expected: expected}
	}
}

// shapeOf returns the broad iteration-classification shape of expr —
// "list" / "object" / "scalar" / "unknown". Used by the conditional
// for_each classifier to compare a non-empty branch against the
// expected shape derived from the empty branch's literal kind.
func (r *Resolver) shapeOf(expr hclsyntax.Expression) string {
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
		if k := r.classifyConditionalForEach(e); k != nil && k.Kind != "" {
			return canonicalShape(k.Kind)
		}
	case *hclsyntax.ScopeTraversalExpr:
		if t := r.ResolveTraversalType(e.Traversal); t != nil {
			return tfTypeShapeName(t)
		}
	case *hclsyntax.RelativeTraversalExpr:
		if src, ok := e.Source.(*hclsyntax.ScopeTraversalExpr); ok {
			full := append(hcl.Traversal(nil), src.Traversal...)
			full = append(full, e.Traversal...)
			if t := r.ResolveTraversalType(full); t != nil {
				return tfTypeShapeName(t)
			}
		}
	}
	if r.m != nil {
		if t := r.m.InferExprType(expr); t != nil && t.Kind != TypeUnknown {
			return tfTypeShapeName(t)
		}
	}
	return "unknown"
}

// ---- helpers ----

// IsEmptyTupleLit reports whether expr is the literal `[]`. Exported
// so the export-side wire-format helpers can stay in render but reuse
// the same recognition logic.
func IsEmptyTupleLit(expr hclsyntax.Expression) bool {
	t, ok := expr.(*hclsyntax.TupleConsExpr)
	return ok && len(t.Exprs) == 0
}

// IsEmptyObjectLit reports whether expr is the literal `{}`.
func IsEmptyObjectLit(expr hclsyntax.Expression) bool {
	o, ok := expr.(*hclsyntax.ObjectConsExpr)
	return ok && len(o.Items) == 0
}

// IsNullLit reports whether expr is the literal `null`.
func IsNullLit(expr hclsyntax.Expression) bool {
	lit, ok := expr.(*hclsyntax.LiteralValueExpr)
	return ok && lit.Val.IsNull()
}

// unwrapPassthrough recognises Terraform idioms where an expression is
// wrapped in a "best-effort" function call that passes the first
// argument's type through:
//
//   - try(X, ...)           — first arg's type
//   - lookup(_, _, default) — third arg (the default)'s type
//   - coalesce(X, ...)      — first arg's type
//
// Returns the inner expression and true when a known passthrough
// wrapper is recognised.
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

// descendType walks the post-prefix steps of a traversal into a
// declared type, descending through attribute steps (object fields,
// map elements) and index steps (list/map/set elements, tuple
// positions, object fields by string-literal key). Returns nil when
// any step doesn't apply to the current type.
func descendType(cur *TFType, steps []hcl.Traverser) *TFType {
	for _, step := range steps {
		if cur == nil {
			return nil
		}
		switch s := step.(type) {
		case hcl.TraverseAttr:
			switch cur.Kind {
			case TypeObject:
				cur = cur.Fields[s.Name]
			case TypeMap:
				cur = cur.Elem
			default:
				return nil
			}
		case hcl.TraverseIndex:
			switch cur.Kind {
			case TypeMap, TypeList, TypeSet:
				cur = cur.Elem
			case TypeObject:
				if s.Key.Type() == cty.String && !s.Key.IsNull() && s.Key.IsKnown() {
					cur = cur.Fields[s.Key.AsString()]
				} else {
					return nil
				}
			case TypeTuple:
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

// classifySingleValueForEach detects the §7 sibling bug class: a
// for_each source that resolves to a single value — typically a
// struct-shaped object field declared via `object({...})`, but also
// scalars — instead of a collection. Recognition requires the source
// to be a traversal expression AND the resolved type to carry a
// declared cty type (HasCty true) — i.e. not inferred from a literal
// value. This excludes valid in-line literal shapes like
// `for_each = { foo = "bar" }`.
//
// Returns (reason, true) when the bug is detected; ("", false) on a
// safe shape.
func classifySingleValueForEach(expr hclsyntax.Expression, t *TFType) (string, bool) {
	if t == nil || !t.HasCty() {
		return "", false
	}
	switch expr.(type) {
	case *hclsyntax.ScopeTraversalExpr, *hclsyntax.RelativeTraversalExpr:
	default:
		return "", false
	}
	switch t.Kind {
	case TypeObject:
		return "for_each source is a single object — Terraform would iterate the object's attributes, not the object itself", true
	case TypeString, TypeNumber, TypeBool:
		return "for_each source is a " + scalarTypeName(t.Kind) + " — single value used where a collection is required", true
	}
	return "", false
}

// branchIterDescription names what Terraform would iterate when a
// non-collection value is wrongly placed in a collection-expected
// slot. Used in classifyConditionalForEach's reason field.
func branchIterDescription(actual string) string {
	switch actual {
	case "object":
		return "the object's attributes as a single-key map"
	case "scalar":
		return "the value as a single-element iteration"
	}
	return "an iteration over the value"
}

// canonicalShape collapses the wider for_each_kind vocabulary into the
// shape vocabulary shapeOf uses (list/object/scalar/unknown).
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
// shapeOf. Mirrors tfTypeKindName but collapses set/tuple into "list"
// and map into "object" so mismatch detection only has three concrete
// buckets to compare.
func tfTypeShapeName(t *TFType) string {
	if t == nil {
		return "unknown"
	}
	switch t.Kind {
	case TypeList, TypeSet, TypeTuple:
		return "list"
	case TypeObject, TypeMap:
		return "object"
	case TypeString, TypeNumber, TypeBool:
		return "scalar"
	}
	return "unknown"
}

// tfTypeKindName maps a TFType to the wire-format kind string. Used
// directly by ClassifyForEach for non-conditional expressions.
func tfTypeKindName(t *TFType) string {
	if t == nil {
		return "unknown"
	}
	switch t.Kind {
	case TypeList:
		return "list"
	case TypeSet:
		return "set"
	case TypeMap:
		return "map"
	case TypeObject:
		return "object"
	case TypeTuple:
		return "tuple"
	case TypeString, TypeNumber, TypeBool:
		return "scalar"
	}
	return "unknown"
}

// scalarTypeName returns the friendly name for a scalar TypeKind.
// Local helper because TypeKind doesn't expose a String method.
func scalarTypeName(k TypeKind) string {
	switch k {
	case TypeString:
		return "string"
	case TypeNumber:
		return "number"
	case TypeBool:
		return "bool"
	}
	return "scalar"
}

// builtinFuncShape maps Terraform built-in function names to their
// for_each shape. A trimmed mirror of builtinFuncReturns: only
// functions that produce a clearly-shaped result are included, so an
// unknown function falls through to "unknown" rather than being given
// a shape it doesn't actually have.
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
