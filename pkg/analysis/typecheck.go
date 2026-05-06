package analysis

import (
	"fmt"
	"sort"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"

	"github.com/dgr237/tflens/pkg/token"
)

// TypeKind classifies a Terraform type constraint or inferred value type.
type TypeKind int

const (
	TypeUnknown TypeKind = iota // could not be determined statically
	TypeString
	TypeNumber
	TypeBool
	TypeNull
	TypeAny
	TypeList
	TypeSet
	TypeMap
	TypeObject
	TypeTuple
)

// TFType represents a Terraform type, possibly parameterised by element or
// field types (e.g. list(string), object({name = string})).
type TFType struct {
	Kind     TypeKind
	Elem     *TFType            // for list / set / map
	Fields   map[string]*TFType // for object
	Elems    []*TFType          // for tuple
	Optional bool               // meaningful only when this type is an object field

	// Cty is the underlying cty.Type when this TFType was derived from a
	// type-constraint expression via ParseTypeExpr. Equals cty.NilType when
	// the TFType came from value inference (InferLiteralType) where the
	// precise cty type isn't available. Used by pkg/diff to run
	// cty.Convert-based assignability checks.
	Cty cty.Type

	// Defaults is the recursive per-attribute default tree returned by
	// typeexpr.TypeConstraintWithDefaults — non-nil only on TFTypes
	// produced by ParseTypeExpr from a type constraint that uses the
	// two-arg `optional(T, default)` form. Walked by pkg/render to emit
	// the variable_type_defaults field on the export. Only set on the
	// root TFType (the top of the type constraint), not on nested Elem
	// / Fields / Elems — the nesting is reflected in Defaults.Children.
	Defaults *typeexpr.Defaults
}

// HasCty reports whether this TFType carries a precise underlying cty.Type
// (i.e. it was produced by ParseTypeExpr, not by inference).
func (t *TFType) HasCty() bool {
	return t != nil && t.Cty != cty.NilType
}

func (t *TFType) String() string {
	if t == nil {
		return "unknown"
	}
	switch t.Kind {
	case TypeString:
		return "string"
	case TypeNumber:
		return "number"
	case TypeBool:
		return "bool"
	case TypeNull:
		return "null"
	case TypeAny:
		return "any"
	case TypeList:
		if t.Elem != nil {
			return fmt.Sprintf("list(%s)", t.Elem)
		}
		return "list(any)"
	case TypeSet:
		if t.Elem != nil {
			return fmt.Sprintf("set(%s)", t.Elem)
		}
		return "set(any)"
	case TypeMap:
		if t.Elem != nil {
			return fmt.Sprintf("map(%s)", t.Elem)
		}
		return "map(any)"
	case TypeObject:
		return "object(...)"
	case TypeTuple:
		return "tuple(...)"
	}
	return "unknown"
}

// TypeCheckError records a type mismatch detected during analysis.
type TypeCheckError struct {
	EntityID string
	Attr     string // which attribute: "default", "for_each", "count"
	Pos      token.Position
	Msg      string
}

func (e TypeCheckError) Error() string {
	return fmt.Sprintf("%s: %s", e.Pos, e.Msg)
}

// TypeErrors returns all type errors found during analysis, sorted by
// source location.
func (m *Module) TypeErrors() []TypeCheckError {
	out := make([]TypeCheckError, len(m.typeErrs))
	copy(out, m.typeErrs)
	sort.Slice(out, func(i, j int) bool {
		a, b := out[i].Pos, out[j].Pos
		if a.File != b.File {
			return a.File < b.File
		}
		if a.Line != b.Line {
			return a.Line < b.Line
		}
		return a.Column < b.Column
	})
	return out
}

// ParseTypeExpr interprets the value of a variable block's "type" attribute
// and returns the corresponding TFType. Unrecognised expressions yield a
// TFType with Kind = TypeUnknown.
//
// Uses TypeConstraintWithDefaults rather than TypeConstraint so the
// two-arg `optional(T, default)` form parses cleanly — TypeConstraint
// errors on the second argument and would mark the entire variable type
// as Unknown. The returned Defaults are discarded (per-attribute type
// defaults are a separate concept from variable-level `default = ...`
// and aren't surfaced today); the optional-attribute markers are
// preserved on the returned cty.Type so cty/json.MarshalType emits
// them as the third element of the object tuple.
func ParseTypeExpr(expr hclsyntax.Expression) *TFType {
	t, defs, diags := typeexpr.TypeConstraintWithDefaults(expr)
	if diags.HasErrors() {
		return &TFType{Kind: TypeUnknown}
	}
	out := ctyToTFType(t)
	if out != nil {
		out.Defaults = defs
	}
	return out
}

// ctyToTFType maps a cty.Type into the internal TFType representation. The
// resulting TFType also carries the original cty.Type via its Cty field so
// callers (notably pkg/diff) can run cty.Convert-based assignability checks
// without re-deriving the type.
func ctyToTFType(t cty.Type) *TFType {
	out := tfTypeShape(t)
	if out != nil {
		out.Cty = t
	}
	return out
}

func tfTypeShape(t cty.Type) *TFType {
	switch {
	case t == cty.String:
		return &TFType{Kind: TypeString}
	case t == cty.Number:
		return &TFType{Kind: TypeNumber}
	case t == cty.Bool:
		return &TFType{Kind: TypeBool}
	case t == cty.DynamicPseudoType:
		return &TFType{Kind: TypeAny}
	case t.IsListType():
		return &TFType{Kind: TypeList, Elem: ctyToTFType(t.ElementType())}
	case t.IsSetType():
		return &TFType{Kind: TypeSet, Elem: ctyToTFType(t.ElementType())}
	case t.IsMapType():
		return &TFType{Kind: TypeMap, Elem: ctyToTFType(t.ElementType())}
	case t.IsObjectType():
		fields := make(map[string]*TFType, len(t.AttributeTypes()))
		for name, at := range t.AttributeTypes() {
			ft := ctyToTFType(at)
			if ft != nil && t.AttributeOptional(name) {
				ft.Optional = true
			}
			fields[name] = ft
		}
		return &TFType{Kind: TypeObject, Fields: fields}
	case t.IsTupleType():
		elems := make([]*TFType, 0, len(t.TupleElementTypes()))
		for _, et := range t.TupleElementTypes() {
			elems = append(elems, ctyToTFType(et))
		}
		return &TFType{Kind: TypeTuple, Elems: elems}
	}
	return &TFType{Kind: TypeUnknown}
}

// ctyValueKind approximates the TFType of a concrete cty.Value, for use in
// error messages (mirrors the old InferLiteralType output).
func ctyValueKind(v cty.Value) *TFType {
	if v.IsNull() {
		return &TFType{Kind: TypeNull}
	}
	t := v.Type()
	if t.IsObjectType() || t.IsMapType() {
		return &TFType{Kind: TypeObject}
	}
	if t.IsTupleType() || t.IsListType() || t.IsSetType() {
		return &TFType{Kind: TypeList}
	}
	return ctyToTFType(t)
}

// builtinFuncReturns maps Terraform built-in function names to the return
// kind we attribute to them when literal evaluation fails. Element types
// are not tracked.
var builtinFuncReturns = map[string]TypeKind{
	// Type conversions
	"toset":    TypeSet,
	"tomap":    TypeMap,
	"tolist":   TypeList,
	"tostring": TypeString,
	"tonumber": TypeNumber,
	"tobool":   TypeBool,

	// Collection: list-producing
	"concat":    TypeList,
	"flatten":   TypeList,
	"reverse":   TypeList,
	"sort":      TypeList,
	"compact":   TypeList,
	"distinct":  TypeList,
	"keys":      TypeList,
	"values":    TypeList,
	"slice":     TypeList,
	"chunklist": TypeList,
	"range":     TypeList,
	"split":     TypeList,
	"regexall":  TypeList,
	"matchkeys": TypeList,

	// Collection: map-producing
	"merge":  TypeMap,
	"zipmap": TypeMap,

	// Numeric
	"length":   TypeNumber,
	"max":      TypeNumber,
	"min":      TypeNumber,
	"abs":      TypeNumber,
	"ceil":     TypeNumber,
	"floor":    TypeNumber,
	"log":      TypeNumber,
	"pow":      TypeNumber,
	"signum":   TypeNumber,
	"sum":      TypeNumber,
	"parseint": TypeNumber,

	// String
	"format":     TypeString,
	"formatlist": TypeList,
	"formatdate": TypeString,
	"join":       TypeString,
	"lower":      TypeString,
	"upper":      TypeString,
	"title":      TypeString,
	"trim":       TypeString,
	"trimspace":  TypeString,
	"trimprefix": TypeString,
	"trimsuffix": TypeString,
	"replace":    TypeString,
	"substr":     TypeString,
	"regex":      TypeString,
	"indent":     TypeString,
	"chomp":      TypeString,
	"strrev":     TypeString,

	// Encoding
	"jsonencode":       TypeString,
	"yamlencode":       TypeString,
	"base64encode":     TypeString,
	"base64decode":     TypeString,
	"urlencode":        TypeString,
	"textencodebase64": TypeString,
	"textdecodebase64": TypeString,

	// Decoding
	"jsondecode": TypeAny,
	"yamldecode": TypeAny,
	"csvdecode":  TypeList,

	// Hash / ID
	"uuid":       TypeString,
	"uuidv5":     TypeString,
	"md5":        TypeString,
	"sha1":       TypeString,
	"sha256":     TypeString,
	"sha512":     TypeString,
	"bcrypt":     TypeString,
	"rsadecrypt": TypeString,

	// Date / time
	"timestamp": TypeString,
	"timeadd":   TypeString,
	"timecmp":   TypeNumber,

	// Network (cidr)
	"cidrhost":    TypeString,
	"cidrnetmask": TypeString,
	"cidrsubnet":  TypeString,
	"cidrsubnets": TypeList,

	// File / path
	"file":             TypeString,
	"fileexists":       TypeBool,
	"filebase64":       TypeString,
	"filebase64sha256": TypeString,
	"filebase64sha512": TypeString,
	"filemd5":          TypeString,
	"filesha1":         TypeString,
	"filesha256":       TypeString,
	"filesha512":       TypeString,
	"abspath":          TypeString,
	"dirname":          TypeString,
	"basename":         TypeString,
	"pathexpand":       TypeString,
	"templatefile":     TypeString,
	"fileset":          TypeSet,

	// Predicates
	"contains":    TypeBool,
	"can":         TypeBool,
	"alltrue":     TypeBool,
	"anytrue":     TypeBool,
	"startswith":  TypeBool,
	"endswith":    TypeBool,
	"issensitive": TypeBool,

	// Pass-through
	"lookup":       TypeAny,
	"element":      TypeAny,
	"try":          TypeAny,
	"coalesce":     TypeAny,
	"coalescelist": TypeList,
	"one":          TypeAny,
	"nonsensitive": TypeAny,
	"sensitive":    TypeAny,
	"defaults":     TypeAny,
}

// InferLiteralType returns the statically-determinable type of expr.
// Returns a TFType with Kind = TypeUnknown for expressions whose type cannot
// be determined without runtime information.
func InferLiteralType(expr hclsyntax.Expression) *TFType {
	if expr == nil {
		return &TFType{Kind: TypeUnknown}
	}
	// Structural shortcuts (avoid cty evaluation for shapes we can classify
	// directly from the AST).
	switch e := expr.(type) {
	case *hclsyntax.TemplateExpr, *hclsyntax.TemplateWrapExpr:
		_ = e
		return &TFType{Kind: TypeString}
	case *hclsyntax.TupleConsExpr:
		return &TFType{Kind: TypeList}
	case *hclsyntax.ObjectConsExpr:
		return &TFType{Kind: TypeObject}
	case *hclsyntax.ForExpr:
		if e.KeyExpr != nil {
			return &TFType{Kind: TypeObject}
		}
		return &TFType{Kind: TypeList}
	case *hclsyntax.FunctionCallExpr:
		if kind, ok := builtinFuncReturns[e.Name]; ok {
			return &TFType{Kind: kind}
		}
	case *hclsyntax.LiteralValueExpr:
		if e.Val.IsNull() {
			return &TFType{Kind: TypeNull}
		}
		return ctyToTFType(e.Val.Type())
	}
	// Final fallback: try constant evaluation for anything else that might
	// actually be a static literal.
	if v, diags := expr.Value(nil); !diags.HasErrors() {
		return ctyValueKind(v)
	}
	return &TFType{Kind: TypeUnknown}
}

// IsTypeCompatible reports whether a value of inferred type actual can
// satisfy the declared type constraint.
func IsTypeCompatible(declared, actual *TFType) bool {
	return isCompatible(declared, actual)
}

func isCompatible(declared, actual *TFType) bool {
	if declared == nil || actual == nil {
		return true
	}
	if declared.Kind == TypeAny {
		return true
	}
	if actual.Kind == TypeUnknown || actual.Kind == TypeNull {
		return true
	}
	if declared.Kind != actual.Kind {
		return false
	}
	if declared.Elem != nil && actual.Elem != nil {
		return isCompatible(declared.Elem, actual.Elem)
	}
	return true
}

// isForEachCompatible reports whether a type is valid as a for_each value.
func isForEachCompatible(t *TFType) bool {
	if t == nil {
		return true
	}
	switch t.Kind {
	case TypeMap, TypeSet, TypeObject, TypeAny, TypeUnknown:
		return true
	}
	return false
}

// ---- type-checking passes ----

// checkDefaultConvertible decides whether the default value is assignable to
// the declared type. First tries cty's conversion machinery on the
// constant-evaluated value (more accurate — correctly accepts things like
// default={} for type=map(string)). When constant evaluation fails (e.g.
// the default is a function call we can't evaluate), falls back to the
// structural InferLiteralType + isCompatible path so we still catch obvious
// mismatches like `default = length(...)` against `type = string`.
func checkDefaultConvertible(e Entity, typeExpr hclsyntax.Expression) *TypeCheckError {
	if e.DefaultExpr == nil || typeExpr == nil {
		return nil
	}
	declared, _, diags := typeexpr.TypeConstraintWithDefaults(typeExpr)
	if diags.HasErrors() {
		return nil
	}
	v, vDiags := e.DefaultExpr.E.Value(nil)
	if !vDiags.HasErrors() {
		if _, err := convert.Convert(v, declared); err == nil {
			return nil
		}
		return &TypeCheckError{
			EntityID: e.ID(),
			Attr:     "default",
			Pos:      posFromRange(e.DefaultExpr.E.Range()),
			Msg: fmt.Sprintf("default value for %s has type %s, want %s",
				e.ID(), ctyValueKind(v), e.DeclaredType),
		}
	}
	// Fallback: structural inference for non-constant defaults.
	inferred := InferLiteralType(e.DefaultExpr.E)
	if isCompatible(e.DeclaredType, inferred) {
		return nil
	}
	return &TypeCheckError{
		EntityID: e.ID(),
		Attr:     "default",
		Pos:      posFromRange(e.DefaultExpr.E.Range()),
		Msg: fmt.Sprintf("default value for %s has type %s, want %s",
			e.ID(), inferred, e.DeclaredType),
	}
}

// typeCheckBodies looks for for_each / count misuses inside every resource,
// data, and module block in file. Runs after entity collection so variable
// type constraints are available for lookup.
func typeCheckBodies(m *Module, file *File) {
	for _, block := range file.Body.Blocks {
		switch block.Type {
		case "resource", "data", "module":
			entityID := blockEntityID(block)
			if entityID == "" {
				continue
			}
			for _, attr := range sortedAttrs(block.Body) {
				switch attr.Name {
				case "for_each":
					m.checkForEach(entityID, attr)
				case "count":
					m.checkCount(entityID, attr)
				}
			}
		}
	}
}

func (m *Module) checkForEach(entityID string, attr *hclsyntax.Attribute) {
	t := m.inferValueType(attr.Expr)
	if !isForEachCompatible(t) {
		m.typeErrs = append(m.typeErrs, TypeCheckError{
			EntityID: entityID,
			Attr:     "for_each",
			Pos:      posFromRange(attr.Expr.Range()),
			Msg: fmt.Sprintf("for_each must be a map or set, got %s (in %s)",
				t, entityID),
		})
		return
	}
	// Top-level §7 conditional rule: catch ternary-fallback patterns
	// that pass the basic type check but smuggle a single value where
	// a collection was clearly intended (the empty-`[]` fallback names
	// the user's expectation). Iterator-scope cases inside dynamic
	// blocks aren't handled here — they need the per-block scope stack
	// the export-side classifier maintains. Top-level resource for_each
	// is the most common surface and the easiest gain.
	if err := m.checkForEachConditionalShape(entityID, attr.Expr); err != nil {
		m.typeErrs = append(m.typeErrs, *err)
	}
}

// checkForEachConditionalShape applies the §7 conditional rule to a
// resource/data/module's `for_each` expression. Recognises ternaries
// where one branch is an empty list `[]` or empty object `{}` literal
// (the fallback shape that names the user's expected iteration kind)
// and flags the case where the non-empty branch resolves to a
// non-collection type. Mirrors the export-side classifyConditionalForEach
// rule but emits a TypeCheckError instead of an export-shape kind.
//
// Returns nil when the expression isn't a recognised ternary shape, or
// when the non-empty branch's type can't be statically resolved
// (TypeUnknown / TypeAny — the conservative interpretation: don't
// false-positive on inputs we can't verify).
func (m *Module) checkForEachConditionalShape(entityID string, expr hclsyntax.Expression) *TypeCheckError {
	cond, ok := expr.(*hclsyntax.ConditionalExpr)
	if !ok {
		return nil
	}
	tEmptyTup := isEmptyTupleLit(cond.TrueResult)
	fEmptyTup := isEmptyTupleLit(cond.FalseResult)
	tEmptyObj := isEmptyObjectLit(cond.TrueResult)
	fEmptyObj := isEmptyObjectLit(cond.FalseResult)

	var nonEmpty hclsyntax.Expression
	var expected string
	switch {
	case tEmptyTup && fEmptyTup, tEmptyObj && fEmptyObj:
		// Both branches empty — degenerate, just an empty iteration
		return nil
	case tEmptyTup || fEmptyTup:
		expected = "list"
		nonEmpty = cond.TrueResult
		if tEmptyTup {
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

	nt := m.inferTraversalType(nonEmpty)
	if nt == nil || nt.Kind == TypeUnknown || nt.Kind == TypeAny {
		return nil
	}

	switch {
	case expected == "list" && nt.Kind == TypeObject:
		return &TypeCheckError{
			EntityID: entityID,
			Attr:     "for_each",
			Pos:      posFromRange(expr.Range()),
			Msg: fmt.Sprintf("for_each ternary's non-empty branch is a single object but fallback is empty list — Terraform would iterate the object's attributes, not a collection (in %s); wrap as [X] if a single-element iteration was intended",
				entityID),
		}
	case expected == "list" && (nt.Kind == TypeString || nt.Kind == TypeNumber || nt.Kind == TypeBool):
		return &TypeCheckError{
			EntityID: entityID,
			Attr:     "for_each",
			Pos:      posFromRange(expr.Range()),
			Msg: fmt.Sprintf("for_each ternary's non-empty branch is a %s but fallback is empty list — single value used where a collection is required (in %s)",
				typeKindFriendly(nt.Kind), entityID),
		}
	case expected == "object" && (nt.Kind == TypeString || nt.Kind == TypeNumber || nt.Kind == TypeBool):
		return &TypeCheckError{
			EntityID: entityID,
			Attr:     "for_each",
			Pos:      posFromRange(expr.Range()),
			Msg: fmt.Sprintf("for_each ternary's non-empty branch is a %s but fallback is empty object — Terraform expects a map or object (in %s)",
				typeKindFriendly(nt.Kind), entityID),
		}
	}
	return nil
}

// inferTraversalType is the index-aware counterpart of inferValueType.
// Where inferValueType uses traversalParts (which truncates at the
// first non-attr step), this walks every step in order so shapes like
// `var.X["primary"].field` resolve to the field's type instead of
// stopping at the index. Same scope as inferValueType — only `var.X`
// roots are resolved against declared types; iterator-scope and
// local-recursion are deferred to the export-side resolver.
//
// Returns TypeUnknown for any non-traversal expression or for
// traversals whose root isn't `var`.
func (m *Module) inferTraversalType(expr hclsyntax.Expression) *TFType {
	if t := InferLiteralType(expr); t != nil && t.Kind != TypeUnknown {
		return t
	}
	stv, ok := expr.(*hclsyntax.ScopeTraversalExpr)
	if !ok || len(stv.Traversal) < 2 {
		return &TFType{Kind: TypeUnknown}
	}
	root, ok := stv.Traversal[0].(hcl.TraverseRoot)
	if !ok || root.Name != "var" {
		return &TFType{Kind: TypeUnknown}
	}
	nameStep, ok := stv.Traversal[1].(hcl.TraverseAttr)
	if !ok {
		return &TFType{Kind: TypeUnknown}
	}
	v, ok := m.byID[(Entity{Kind: KindVariable, Name: nameStep.Name}).ID()]
	if !ok || v.DeclaredType == nil {
		return &TFType{Kind: TypeUnknown}
	}
	return descendTraversal(v.DeclaredType, stv.Traversal[2:])
}

// descendTraversal walks the post-prefix steps of a traversal into a
// declared type, descending through attribute steps (object fields,
// map elements) and index steps (list/map/set elements, tuple
// positions, object fields by string-literal key). Returns TypeUnknown
// when any step doesn't apply to the current type.
func descendTraversal(cur *TFType, steps []hcl.Traverser) *TFType {
	for _, step := range steps {
		if cur == nil {
			return &TFType{Kind: TypeUnknown}
		}
		switch s := step.(type) {
		case hcl.TraverseAttr:
			switch cur.Kind {
			case TypeObject:
				cur = cur.Fields[s.Name]
			case TypeMap:
				cur = cur.Elem
			case TypeAny:
				return &TFType{Kind: TypeAny}
			default:
				return &TFType{Kind: TypeUnknown}
			}
		case hcl.TraverseIndex:
			switch cur.Kind {
			case TypeMap, TypeList, TypeSet:
				cur = cur.Elem
			case TypeObject:
				if s.Key.Type() == cty.String && !s.Key.IsNull() && s.Key.IsKnown() {
					cur = cur.Fields[s.Key.AsString()]
				} else {
					return &TFType{Kind: TypeUnknown}
				}
			case TypeTuple:
				if s.Key.Type() == cty.Number && !s.Key.IsNull() && s.Key.IsKnown() {
					n, _ := s.Key.AsBigFloat().Int64()
					if n >= 0 && int(n) < len(cur.Elems) {
						cur = cur.Elems[n]
					} else {
						return &TFType{Kind: TypeUnknown}
					}
				} else {
					return &TFType{Kind: TypeUnknown}
				}
			case TypeAny:
				return &TFType{Kind: TypeAny}
			default:
				return &TFType{Kind: TypeUnknown}
			}
		default:
			return &TFType{Kind: TypeUnknown}
		}
	}
	if cur == nil {
		return &TFType{Kind: TypeUnknown}
	}
	return cur
}

// isEmptyTupleLit reports whether expr is the literal `[]`. Used by
// checkForEachConditionalShape to recognise the fallback branch.
func isEmptyTupleLit(expr hclsyntax.Expression) bool {
	t, ok := expr.(*hclsyntax.TupleConsExpr)
	return ok && len(t.Exprs) == 0
}

// isEmptyObjectLit reports whether expr is the literal `{}`. Used by
// checkForEachConditionalShape when the expected shape is object/map.
func isEmptyObjectLit(expr hclsyntax.Expression) bool {
	o, ok := expr.(*hclsyntax.ObjectConsExpr)
	return ok && len(o.Items) == 0
}

// typeKindFriendly returns the user-facing name of a scalar type kind.
// Only used in checkForEachConditionalShape error messages — TFType
// has String() but it returns "string" / "number" / "bool" already
// for these cases, so this is essentially a guard against rendering
// an unexpected kind into the message.
func typeKindFriendly(k TypeKind) string {
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

func (m *Module) checkCount(entityID string, attr *hclsyntax.Attribute) {
	t := m.inferValueType(attr.Expr)
	switch t.Kind {
	case TypeList, TypeSet, TypeMap, TypeObject, TypeTuple, TypeBool:
		m.typeErrs = append(m.typeErrs, TypeCheckError{
			EntityID: entityID,
			Attr:     "count",
			Pos:      posFromRange(attr.Expr.Range()),
			Msg: fmt.Sprintf("count must be a number, got %s (in %s)",
				t, entityID),
		})
	}
}

// InferExprType returns the best-effort type of expr in the context of this
// module. First tries literal inference, then resolves var.X references to
// the variable's declared type.
func (m *Module) InferExprType(expr hclsyntax.Expression) *TFType {
	return m.inferValueType(expr)
}

func (m *Module) inferValueType(expr hclsyntax.Expression) *TFType {
	if t := InferLiteralType(expr); t.Kind != TypeUnknown {
		return t
	}
	if stv, ok := expr.(*hclsyntax.ScopeTraversalExpr); ok {
		parts := traversalParts(stv.Traversal)
		if len(parts) >= 2 && parts[0] == "var" {
			varID := (Entity{Kind: KindVariable, Name: parts[1]}).ID()
			if e, ok := m.byID[varID]; ok && e.DeclaredType != nil {
				return descendDeclaredType(e.DeclaredType, parts[2:])
			}
		}
	}
	return &TFType{Kind: TypeUnknown}
}

// descendDeclaredType walks attr-traversal parts (e.g. `.property` or
// `.config.property`) into a declared type. Returns the leaf type when
// the path resolves cleanly; returns TypeUnknown when a step doesn't
// match (e.g. unknown field on an object) so callers don't false-positive
// against an unrelated parent type. Handles object-field access and
// map-style dotted access (HCL2 treats `m.k` as `m["k"]` for maps).
func descendDeclaredType(t *TFType, attrPath []string) *TFType {
	cur := t
	for _, name := range attrPath {
		if cur == nil {
			return &TFType{Kind: TypeUnknown}
		}
		switch cur.Kind {
		case TypeObject:
			next, ok := cur.Fields[name]
			if !ok {
				return &TFType{Kind: TypeUnknown}
			}
			cur = next
		case TypeMap:
			if cur.Elem == nil {
				return &TFType{Kind: TypeUnknown}
			}
			cur = cur.Elem
		case TypeAny:
			return &TFType{Kind: TypeAny}
		default:
			return &TFType{Kind: TypeUnknown}
		}
	}
	return cur
}

// blockEntityID returns the canonical entity ID for a resource/data/module
// block. Returns an empty string if the block has the wrong label count.
func blockEntityID(block *hclsyntax.Block) string {
	switch block.Type {
	case "resource":
		if len(block.Labels) == 2 {
			return (Entity{Kind: KindResource, Type: block.Labels[0], Name: block.Labels[1]}).ID()
		}
	case "data":
		if len(block.Labels) == 2 {
			return (Entity{Kind: KindData, Type: block.Labels[0], Name: block.Labels[1]}).ID()
		}
	case "module":
		if len(block.Labels) == 1 {
			return (Entity{Kind: KindModule, Name: block.Labels[0]}).ID()
		}
	}
	return ""
}
