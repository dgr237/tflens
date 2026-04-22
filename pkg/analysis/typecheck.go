package analysis

import (
	"fmt"
	"sort"
	"github.com/dgr237/tflens/pkg/ast"
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
	Optional bool               // meaningful only when this type is an object field (set via optional(...))
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

// ParseType interprets a type-constraint expression — the value of a variable
// block's "type" attribute — and returns the corresponding TFType.
// Unrecognised expressions yield a TFType with Kind = TypeUnknown.
func ParseType(expr ast.Expr) *TFType {
	switch e := expr.(type) {
	case *ast.RefExpr:
		if len(e.Parts) == 1 {
			switch e.Parts[0] {
			case "string":
				return &TFType{Kind: TypeString}
			case "number":
				return &TFType{Kind: TypeNumber}
			case "bool":
				return &TFType{Kind: TypeBool}
			case "any":
				return &TFType{Kind: TypeAny}
			}
		}
	case *ast.CallExpr:
		switch e.Name {
		case "list":
			t := &TFType{Kind: TypeList}
			if len(e.Args) == 1 {
				t.Elem = ParseType(e.Args[0])
			}
			return t
		case "set":
			t := &TFType{Kind: TypeSet}
			if len(e.Args) == 1 {
				t.Elem = ParseType(e.Args[0])
			}
			return t
		case "map":
			t := &TFType{Kind: TypeMap}
			if len(e.Args) == 1 {
				t.Elem = ParseType(e.Args[0])
			}
			return t
		case "object":
			t := &TFType{Kind: TypeObject, Fields: make(map[string]*TFType)}
			if len(e.Args) == 1 {
				if obj, ok := e.Args[0].(*ast.ObjectExpr); ok {
					for _, item := range obj.Items {
						key := objectKeyName(item.Key)
						if key != "" {
							t.Fields[key] = parseObjectFieldType(item.Value)
						}
					}
				}
			}
			return t
		case "optional":
			// optional(T) outside an object body — treat as the inner type,
			// marked optional. (Strictly Terraform requires optional() only
			// inside object(); we're lenient.)
			if len(e.Args) >= 1 {
				inner := ParseType(e.Args[0])
				if inner != nil {
					inner.Optional = true
				}
				return inner
			}
		case "tuple":
			t := &TFType{Kind: TypeTuple}
			if len(e.Args) == 1 {
				if tup, ok := e.Args[0].(*ast.TupleExpr); ok {
					for _, item := range tup.Items {
						t.Elems = append(t.Elems, ParseType(item))
					}
				}
			}
			return t
		}
	}
	return &TFType{Kind: TypeUnknown}
}

// parseObjectFieldType parses an object-field type expression, recognising
// the `optional(T)` and `optional(T, default)` wrappers that mark the field
// as optional. Returns a TFType whose Optional flag is set when the wrapper
// was present.
func parseObjectFieldType(expr ast.Expr) *TFType {
	if call, ok := expr.(*ast.CallExpr); ok && call.Name == "optional" && len(call.Args) >= 1 {
		t := ParseType(call.Args[0])
		if t != nil {
			t.Optional = true
		}
		return t
	}
	return ParseType(expr)
}

// objectKeyName extracts a string field name from an object literal key
// expression. Keys may be bare identifiers (RefExpr) or string literals.
func objectKeyName(expr ast.Expr) string {
	switch k := expr.(type) {
	case *ast.RefExpr:
		if len(k.Parts) == 1 {
			return k.Parts[0]
		}
	case *ast.LiteralExpr:
		if s, ok := k.Value.(string); ok {
			return s
		}
	}
	return ""
}

// builtinFuncReturns maps built-in Terraform function names to their return
// type kind. Element types are not tracked — a function returning
// "list(string)" is recorded as TypeList, which is sufficient for the checks
// we perform (e.g. rejecting list-returning calls in for_each context).
//
// Argument types are deliberately not tracked: many Terraform functions are
// polymorphic or variadic, and checking argument types duplicates Terraform's
// own runtime validation with much lower fidelity.
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

	// Decoding returns unknown structure
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

	// Pass-through (return type depends on args; treat as any)
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
// be determined without runtime information (refs to resources, unknown
// function calls, conditionals, etc.).
func InferLiteralType(expr ast.Expr) *TFType {
	switch e := expr.(type) {
	case *ast.LiteralExpr:
		switch e.Value.(type) {
		case string:
			return &TFType{Kind: TypeString}
		case float64:
			return &TFType{Kind: TypeNumber}
		case bool:
			return &TFType{Kind: TypeBool}
		case nil:
			return &TFType{Kind: TypeNull}
		}
	case *ast.TemplateExpr:
		return &TFType{Kind: TypeString}
	case *ast.TupleExpr:
		return &TFType{Kind: TypeList}
	case *ast.ObjectExpr:
		return &TFType{Kind: TypeObject}
	case *ast.CallExpr:
		if kind, ok := builtinFuncReturns[e.Name]; ok {
			return &TFType{Kind: kind}
		}
	}
	return &TFType{Kind: TypeUnknown}
}

// IsTypeCompatible reports whether a value of inferred type actual can satisfy
// the declared type constraint. TypeUnknown and TypeNull are always allowed;
// TypeAny accepts everything. Parameterised types are checked recursively on
// their element type.
func IsTypeCompatible(declared, actual *TFType) bool {
	return isCompatible(declared, actual)
}

// isCompatible reports whether a value of inferred type actual can satisfy
// the declared type constraint. TypeUnknown and TypeNull are always allowed;
// TypeAny accepts everything.
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
// Terraform requires a map or a set; lists are a common mistake. Object
// literals ({k = v, ...}) are accepted because Terraform treats them as maps
// when used as for_each.
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

// ---- type-checking pass ----

// typeCheckBodies looks for for_each / count misuses inside every resource,
// data, and module block in body. Runs after entity collection so that
// variable type constraints are available for lookup.
func typeCheckBodies(m *Module, body *ast.Body) {
	for _, node := range body.Nodes {
		block, ok := node.(*ast.Block)
		if !ok {
			continue
		}
		switch block.Type {
		case "resource", "data", "module":
			entityID := blockEntityID(block)
			if entityID == "" {
				continue
			}
			for _, n := range block.Body.Nodes {
				attr, ok := n.(*ast.Attribute)
				if !ok {
					continue
				}
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

func (m *Module) checkForEach(entityID string, attr *ast.Attribute) {
	t := m.inferValueType(attr.Value)
	if isForEachCompatible(t) {
		return
	}
	m.typeErrs = append(m.typeErrs, TypeCheckError{
		EntityID: entityID,
		Attr:     "for_each",
		Pos:      ast.NodePos(attr.Value),
		Msg: fmt.Sprintf("for_each must be a map or set, got %s (in %s)",
			t, entityID),
	})
}

func (m *Module) checkCount(entityID string, attr *ast.Attribute) {
	t := m.inferValueType(attr.Value)
	// Flag only clearly-wrong kinds. Strings are coercible to numbers by
	// Terraform so we allow them; TypeUnknown/TypeAny are also allowed.
	switch t.Kind {
	case TypeList, TypeSet, TypeMap, TypeObject, TypeTuple, TypeBool:
		m.typeErrs = append(m.typeErrs, TypeCheckError{
			EntityID: entityID,
			Attr:     "count",
			Pos:      ast.NodePos(attr.Value),
			Msg: fmt.Sprintf("count must be a number, got %s (in %s)",
				t, entityID),
		})
	}
}

// InferExprType returns the best-effort type of expr in the context of this
// module. It tries literal inference first (including built-in function return
// types), then resolves var.X references to the variable's declared type if
// available. Returns TypeUnknown when the type cannot be determined.
func (m *Module) InferExprType(expr ast.Expr) *TFType {
	return m.inferValueType(expr)
}

// inferValueType returns the best-effort type of expr. It first tries literal
// inference, then resolves var.X to the variable's declared type if known.
func (m *Module) inferValueType(expr ast.Expr) *TFType {
	if t := InferLiteralType(expr); t.Kind != TypeUnknown {
		return t
	}
	if ref, ok := expr.(*ast.RefExpr); ok && len(ref.Parts) >= 2 && ref.Parts[0] == "var" {
		varID := Entity{Kind: KindVariable, Name: ref.Parts[1]}.ID()
		if e, ok := m.byID[varID]; ok && e.DeclaredType != nil {
			return e.DeclaredType
		}
	}
	return &TFType{Kind: TypeUnknown}
}

// blockEntityID returns the canonical entity ID for a resource/data/module
// block. Returns an empty string if the block has the wrong label count.
func blockEntityID(block *ast.Block) string {
	switch block.Type {
	case "resource":
		if len(block.Labels) == 2 {
			return Entity{Kind: KindResource, Type: block.Labels[0], Name: block.Labels[1]}.ID()
		}
	case "data":
		if len(block.Labels) == 2 {
			return Entity{Kind: KindData, Type: block.Labels[0], Name: block.Labels[1]}.ID()
		}
	case "module":
		if len(block.Labels) == 1 {
			return Entity{Kind: KindModule, Name: block.Labels[0]}.ID()
		}
	}
	return ""
}
