package analysis

import (
	"fmt"
	"sort"

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
func ParseTypeExpr(expr hclsyntax.Expression) *TFType {
	t, diags := typeexpr.TypeConstraint(expr)
	if diags.HasErrors() {
		return &TFType{Kind: TypeUnknown}
	}
	return ctyToTFType(t)
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
	declared, diags := typeexpr.TypeConstraint(typeExpr)
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
	if isForEachCompatible(t) {
		return
	}
	m.typeErrs = append(m.typeErrs, TypeCheckError{
		EntityID: entityID,
		Attr:     "for_each",
		Pos:      posFromRange(attr.Expr.Range()),
		Msg: fmt.Sprintf("for_each must be a map or set, got %s (in %s)",
			t, entityID),
	})
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
				return e.DeclaredType
			}
		}
	}
	return &TFType{Kind: TypeUnknown}
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
