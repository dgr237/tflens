// Package hclbridge is a prototype of an alternative parser path built on
// github.com/hashicorp/hcl/v2. It exists to evaluate whether tflens could
// replace its hand-rolled lexer/parser with the upstream library.
//
// Scope: enough to run the inventory command byte-identically and to populate
// variable-block details (type constraint, default presence, sensitive /
// nullable, validation/precondition/postcondition counts) plus produce the
// same default-value type-compatibility errors the analysis package reports.
// Deeper fields (ForEachExpr, ModuleArgs, etc.) are left nil.
package hclbridge

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/ext/typeexpr"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/token"
)

// Load parses all .tf files at path (file or directory) and returns the
// declared entities in declaration order. Populates only Kind, Type, Name,
// Pos for non-variable entities; variables get their full scalar shape.
func Load(path string) ([]analysis.Entity, error) {
	r, err := loadAll(path)
	if err != nil {
		return nil, err
	}
	return r.Entities, nil
}

// LoadWithDetails additionally returns default-value type-compatibility
// errors — the same ones analysis.Module.TypeErrors() returns for variable
// defaults.
func LoadWithDetails(path string) ([]analysis.Entity, []analysis.TypeCheckError, error) {
	r, err := loadAll(path)
	if err != nil {
		return nil, nil, err
	}
	return r.Entities, r.TypeErrors, nil
}

// LoadResult is the full shape produced by LoadGraph: entities plus the
// dependency graph and reference-validation errors.
type LoadResult struct {
	Entities     []analysis.Entity
	Dependencies map[string]map[string]bool // from-ID → set of to-IDs
	ValErrors    []analysis.ValidationError
	TypeErrors   []analysis.TypeCheckError
}

// LoadGraph parses at path and returns entities plus dependency edges and
// undefined-reference errors — the subset of analysis.Module that the
// validate / deps / impact / graph commands consume.
func LoadGraph(path string) (LoadResult, error) {
	return loadAll(path)
}

func loadAll(path string) (LoadResult, error) {
	files, err := listFiles(path)
	if err != nil {
		return LoadResult{}, err
	}

	parser := hclparse.NewParser()
	var bodies []*hclsyntax.Body
	for _, f := range files {
		src, err := os.ReadFile(f)
		if err != nil {
			return LoadResult{}, err
		}
		file, diags := parser.ParseHCL(src, f)
		if diags.HasErrors() {
			return LoadResult{}, fmt.Errorf("parse %s: %s", f, diags.Error())
		}
		body, ok := file.Body.(*hclsyntax.Body)
		if !ok {
			return LoadResult{}, fmt.Errorf("%s: unexpected body type %T", f, file.Body)
		}
		bodies = append(bodies, body)
	}

	var entities []analysis.Entity
	var typeErrs []analysis.TypeCheckError
	for _, body := range bodies {
		es, tes := collectEntities(body)
		entities = append(entities, es...)
		typeErrs = append(typeErrs, tes...)
	}

	byID := make(map[string]bool, len(entities))
	for _, e := range entities {
		byID[e.ID()] = true
	}

	deps := make(map[string]map[string]bool)
	var valErrs []analysis.ValidationError
	for _, body := range bodies {
		collectDeps(body, byID, deps, &valErrs)
	}

	return LoadResult{
		Entities:     entities,
		Dependencies: deps,
		ValErrors:    valErrs,
		TypeErrors:   typeErrs,
	}, nil
}

func listFiles(path string) ([]string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() {
		return []string{path}, nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return nil, err
	}
	var files []string
	for _, ent := range entries {
		if ent.IsDir() {
			continue
		}
		name := ent.Name()
		if strings.HasSuffix(name, ".tf") && !strings.HasSuffix(name, ".tftest.tf") {
			files = append(files, filepath.Join(path, name))
		}
	}
	sort.Strings(files)
	return files, nil
}

func collectEntities(body *hclsyntax.Body) ([]analysis.Entity, []analysis.TypeCheckError) {
	var out []analysis.Entity
	var typeErrs []analysis.TypeCheckError

	for _, block := range body.Blocks {
		switch block.Type {
		case "resource":
			if len(block.Labels) == 2 {
				out = append(out, analysis.Entity{
					Kind: analysis.KindResource,
					Type: block.Labels[0],
					Name: block.Labels[1],
					Pos:  posFromRange(block.DefRange()),
				})
			}
		case "data":
			if len(block.Labels) == 2 {
				out = append(out, analysis.Entity{
					Kind: analysis.KindData,
					Type: block.Labels[0],
					Name: block.Labels[1],
					Pos:  posFromRange(block.DefRange()),
				})
			}
		case "variable":
			if len(block.Labels) == 1 {
				e, te := variableEntity(block)
				out = append(out, e)
				typeErrs = append(typeErrs, te...)
			}
		case "output":
			if len(block.Labels) == 1 {
				out = append(out, analysis.Entity{
					Kind: analysis.KindOutput,
					Name: block.Labels[0],
					Pos:  posFromRange(block.DefRange()),
				})
			}
		case "module":
			if len(block.Labels) == 1 {
				out = append(out, analysis.Entity{
					Kind: analysis.KindModule,
					Name: block.Labels[0],
					Pos:  posFromRange(block.DefRange()),
				})
			}
		case "locals":
			out = append(out, localsEntities(block.Body)...)
		}
	}
	return out, typeErrs
}

// variableEntity builds an Entity for a variable block and, if the declared
// type and default are both present, checks that the default is convertible
// to the declared type.
func variableEntity(block *hclsyntax.Block) (analysis.Entity, []analysis.TypeCheckError) {
	e := analysis.Entity{
		Kind: analysis.KindVariable,
		Name: block.Labels[0],
		Pos:  posFromRange(block.DefRange()),
	}

	var declaredCty cty.Type
	haveDeclared := false
	var defaultAttr *hclsyntax.Attribute

	for name, attr := range block.Body.Attributes {
		switch name {
		case "type":
			t, diags := typeexpr.TypeConstraint(attr.Expr)
			if !diags.HasErrors() {
				declaredCty = t
				haveDeclared = true
				e.DeclaredType = ctyToTFType(t)
			}
		case "default":
			e.HasDefault = true
			defaultAttr = attr
		case "nullable":
			if b, ok := asBool(attr.Expr); ok && !b {
				e.NonNullable = true
			}
		case "sensitive":
			if b, ok := asBool(attr.Expr); ok && b {
				e.Sensitive = true
			}
		}
	}

	for _, b := range block.Body.Blocks {
		switch b.Type {
		case "validation":
			e.Validations++
		case "precondition":
			e.Preconditions++
		case "postcondition":
			e.Postconditions++
		}
	}

	var typeErrs []analysis.TypeCheckError
	if haveDeclared && defaultAttr != nil {
		if te, ok := checkDefaultAgainstType(e.ID(), defaultAttr, declaredCty, e.DeclaredType); ok {
			typeErrs = append(typeErrs, te)
		}
	}
	return e, typeErrs
}

// checkDefaultAgainstType attempts to evaluate the default expression as a
// constant (empty EvalContext) and convert the result to the declared type.
// Returns a TypeCheckError when conversion fails in a way that matches the
// analysis package's output format. Returns ok=false when the check can't be
// performed statically (expression references variables/functions).
func checkDefaultAgainstType(entityID string, attr *hclsyntax.Attribute, declared cty.Type, declaredTF *analysis.TFType) (analysis.TypeCheckError, bool) {
	val, diags := attr.Expr.Value(nil)
	if diags.HasErrors() {
		return analysis.TypeCheckError{}, false
	}
	if _, err := convert.Convert(val, declared); err == nil {
		return analysis.TypeCheckError{}, false
	}
	inferred := ctyValueToTFType(val)
	return analysis.TypeCheckError{
		EntityID: entityID,
		Attr:     "default",
		Pos:      posFromRange(attr.Expr.Range()),
		Msg: fmt.Sprintf("default value for %s has type %s, want %s",
			entityID, inferred, declaredTF),
	}, true
}

// ctyToTFType maps a cty.Type (from typeexpr.TypeConstraint) into the
// internal TFType representation so downstream code keeps working unchanged.
func ctyToTFType(t cty.Type) *analysis.TFType {
	switch {
	case t == cty.String:
		return &analysis.TFType{Kind: analysis.TypeString}
	case t == cty.Number:
		return &analysis.TFType{Kind: analysis.TypeNumber}
	case t == cty.Bool:
		return &analysis.TFType{Kind: analysis.TypeBool}
	case t == cty.DynamicPseudoType:
		return &analysis.TFType{Kind: analysis.TypeAny}
	case t.IsListType():
		return &analysis.TFType{Kind: analysis.TypeList, Elem: ctyToTFType(t.ElementType())}
	case t.IsSetType():
		return &analysis.TFType{Kind: analysis.TypeSet, Elem: ctyToTFType(t.ElementType())}
	case t.IsMapType():
		return &analysis.TFType{Kind: analysis.TypeMap, Elem: ctyToTFType(t.ElementType())}
	case t.IsObjectType():
		fields := make(map[string]*analysis.TFType, len(t.AttributeTypes()))
		for name, at := range t.AttributeTypes() {
			ft := ctyToTFType(at)
			if ft != nil && t.AttributeOptional(name) {
				ft.Optional = true
			}
			fields[name] = ft
		}
		return &analysis.TFType{Kind: analysis.TypeObject, Fields: fields}
	case t.IsTupleType():
		elems := make([]*analysis.TFType, 0, len(t.TupleElementTypes()))
		for _, et := range t.TupleElementTypes() {
			elems = append(elems, ctyToTFType(et))
		}
		return &analysis.TFType{Kind: analysis.TypeTuple, Elems: elems}
	}
	return &analysis.TFType{Kind: analysis.TypeUnknown}
}

// ctyValueToTFType is used only for error-message formatting: it produces a
// TFType that mirrors what InferLiteralType would say about the value.
func ctyValueToTFType(v cty.Value) *analysis.TFType {
	if v.IsNull() {
		return &analysis.TFType{Kind: analysis.TypeNull}
	}
	t := v.Type()
	if t.IsObjectType() || t.IsMapType() {
		return &analysis.TFType{Kind: analysis.TypeObject}
	}
	if t.IsTupleType() || t.IsListType() || t.IsSetType() {
		return &analysis.TFType{Kind: analysis.TypeList}
	}
	return ctyToTFType(t)
}

// asBool evaluates expr as a constant boolean; returns ok=false when the
// expression isn't a static bool literal.
func asBool(expr hclsyntax.Expression) (bool, bool) {
	v, diags := expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || v.Type() != cty.Bool {
		return false, false
	}
	return v.True(), true
}

// localsEntities flattens a locals { ... } block. hclsyntax stores attributes
// in an unordered map, so sort by source position to preserve declaration
// order.
func localsEntities(body *hclsyntax.Body) []analysis.Entity {
	attrs := make([]*hclsyntax.Attribute, 0, len(body.Attributes))
	for _, a := range body.Attributes {
		attrs = append(attrs, a)
	}
	sort.Slice(attrs, func(i, j int) bool {
		return attrs[i].SrcRange.Start.Byte < attrs[j].SrcRange.Start.Byte
	})
	out := make([]analysis.Entity, 0, len(attrs))
	for _, a := range attrs {
		out = append(out, analysis.Entity{
			Kind: analysis.KindLocal,
			Name: a.Name,
			Pos:  posFromRange(a.NameRange),
		})
	}
	return out
}

func posFromRange(r hcl.Range) token.Position {
	return token.Position{
		File:   r.Filename,
		Line:   r.Start.Line,
		Column: r.Start.Column,
	}
}

// ExprText returns a deterministic canonical text form of expr, suitable as
// the comparison key used in pkg/diff. The canonicalisation runs hclwrite's
// formatter over the source bytes of the expression, so inputs that differ
// only in whitespace / quoting produce the same key, while inputs that
// differ structurally don't.
//
// Both sides of a diff must use the same src buffer for the expressions
// they contain. This matches the existing pattern: pkg/diff compares
// printer.PrintExpr(old) against printer.PrintExpr(new) and treats equal
// strings as unchanged.
func ExprText(expr hclsyntax.Expression, src []byte) string {
	if expr == nil {
		return ""
	}
	r := expr.Range()
	if r.Start.Byte < 0 || r.End.Byte > len(src) || r.Start.Byte > r.End.Byte {
		return ""
	}
	raw := src[r.Start.Byte:r.End.Byte]
	formatted := hclwrite.Format(raw)
	return strings.TrimSpace(string(formatted))
}

// collectDeps walks one file body, mirroring analysis.collectDependencies:
// it walks resource/data/output/module blocks and per-attribute locals, and
// for every reference inside records either a dep edge (target is declared)
// or a ValidationError (target is a var/local/module/data prefix with no
// matching declaration). Bare resource-style references are not validated,
// to avoid false positives from for-expression iteration variables.
func collectDeps(body *hclsyntax.Body, byID map[string]bool, deps map[string]map[string]bool, valErrs *[]analysis.ValidationError) {
	for _, block := range body.Blocks {
		switch block.Type {
		case "resource":
			if len(block.Labels) == 2 {
				id := (analysis.Entity{Kind: analysis.KindResource, Type: block.Labels[0], Name: block.Labels[1]}).ID()
				walkBody(block.Body, id, byID, deps, valErrs)
			}
		case "data":
			if len(block.Labels) == 2 {
				id := (analysis.Entity{Kind: analysis.KindData, Type: block.Labels[0], Name: block.Labels[1]}).ID()
				walkBody(block.Body, id, byID, deps, valErrs)
			}
		case "output":
			if len(block.Labels) == 1 {
				id := (analysis.Entity{Kind: analysis.KindOutput, Name: block.Labels[0]}).ID()
				walkBody(block.Body, id, byID, deps, valErrs)
			}
		case "module":
			if len(block.Labels) == 1 {
				id := (analysis.Entity{Kind: analysis.KindModule, Name: block.Labels[0]}).ID()
				walkBody(block.Body, id, byID, deps, valErrs)
			}
		case "locals":
			// Each local attribute is its own entity; scope references to it.
			for _, attr := range block.Body.Attributes {
				id := (analysis.Entity{Kind: analysis.KindLocal, Name: attr.Name}).ID()
				walkExpr(attr.Expr, id, byID, deps, valErrs)
			}
		}
	}
}

// walkBody recurses through a body, calling walkExpr on every attribute
// expression. Nested blocks (e.g. `ingress { ... }` inside a resource) are
// descended into but their references are still attributed to fromID.
func walkBody(body *hclsyntax.Body, fromID string, byID map[string]bool, deps map[string]map[string]bool, valErrs *[]analysis.ValidationError) {
	for _, attr := range body.Attributes {
		walkExpr(attr.Expr, fromID, byID, deps, valErrs)
	}
	for _, b := range body.Blocks {
		walkBody(b.Body, fromID, byID, deps, valErrs)
	}
}

// walkExpr collects every traversal reachable from expr (hclsyntax does the
// recursion for us via Expression.Variables) and classifies each one.
func walkExpr(expr hclsyntax.Expression, fromID string, byID map[string]bool, deps map[string]map[string]bool, valErrs *[]analysis.ValidationError) {
	for _, trav := range expr.Variables() {
		parts := traversalParts(trav)
		pos := posFromRange(trav.SourceRange())
		recordRef(fromID, parts, pos, byID, deps, valErrs)
	}
}

// traversalParts flattens a hcl.Traversal into the flat []string form that
// the hand-rolled RefExpr.Parts uses. We only collect the root + any leading
// TraverseAttr steps; the first index/splat ends the chain, because entity
// classification never needs anything past that.
func traversalParts(trav hcl.Traversal) []string {
	if len(trav) == 0 {
		return nil
	}
	var parts []string
	for i, step := range trav {
		switch s := step.(type) {
		case hcl.TraverseRoot:
			if i != 0 {
				return parts
			}
			parts = append(parts, s.Name)
		case hcl.TraverseAttr:
			parts = append(parts, s.Name)
		default:
			return parts
		}
	}
	return parts
}

// recordRef is the bridge analogue of analysis.Module.recordRef.
func recordRef(fromID string, parts []string, pos token.Position, byID map[string]bool, deps map[string]map[string]bool, valErrs *[]analysis.ValidationError) {
	if dep, ok := classifyRef(parts, byID); ok {
		addDep(deps, fromID, dep)
		return
	}
	if len(parts) < 2 {
		return
	}
	var missing string
	switch parts[0] {
	case "var":
		missing = (analysis.Entity{Kind: analysis.KindVariable, Name: parts[1]}).ID()
	case "local":
		missing = (analysis.Entity{Kind: analysis.KindLocal, Name: parts[1]}).ID()
	case "module":
		missing = (analysis.Entity{Kind: analysis.KindModule, Name: parts[1]}).ID()
	case "data":
		if len(parts) >= 3 {
			missing = (analysis.Entity{Kind: analysis.KindData, Type: parts[1], Name: parts[2]}).ID()
		}
	}
	if missing == "" {
		return
	}
	*valErrs = append(*valErrs, analysis.ValidationError{
		EntityID: fromID,
		Ref:      missing,
		Pos:      pos,
	})
}

// classifyRef returns the canonical entity ID the reference resolves to, if
// it points to a known entity in byID.
func classifyRef(parts []string, byID map[string]bool) (string, bool) {
	if len(parts) < 2 {
		return "", false
	}
	switch parts[0] {
	case "var":
		id := (analysis.Entity{Kind: analysis.KindVariable, Name: parts[1]}).ID()
		if byID[id] {
			return id, true
		}
	case "local":
		id := (analysis.Entity{Kind: analysis.KindLocal, Name: parts[1]}).ID()
		if byID[id] {
			return id, true
		}
	case "module":
		id := (analysis.Entity{Kind: analysis.KindModule, Name: parts[1]}).ID()
		if byID[id] {
			return id, true
		}
	case "data":
		if len(parts) >= 3 {
			id := (analysis.Entity{Kind: analysis.KindData, Type: parts[1], Name: parts[2]}).ID()
			if byID[id] {
				return id, true
			}
		}
	default:
		id := (analysis.Entity{Kind: analysis.KindResource, Type: parts[0], Name: parts[1]}).ID()
		if byID[id] {
			return id, true
		}
	}
	return "", false
}

func addDep(deps map[string]map[string]bool, from, to string) {
	if from == to {
		return
	}
	set, ok := deps[from]
	if !ok {
		set = make(map[string]bool)
		deps[from] = set
	}
	set[to] = true
}
