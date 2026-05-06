package analysis

import (
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/dgr237/tflens/pkg/providerschema"
)

// CheckResourceAttrRefs walks every expression in m and validates
// each resource / data-source attribute reference against the
// supplied provider schema. Adds a ValidationError to m for each
// reference whose attribute path doesn't resolve in the schema.
//
// No-op when schema is nil — the call is unconditionally safe even
// when the user didn't supply `--provider-schema`.
//
// False-positive guards:
//
//   - Skips resource refs whose type isn't covered by the schema
//     (multi-cloud configs supplying only an AWS schema get
//     attribute validation for AWS resources, silent passthrough
//     for the rest).
//   - Skips refs whose name doesn't match any declared entity in
//     this module (those are already caught by the existing
//     undefined-reference pass; double-flagging would be noise).
//   - Skips refs whose path includes index/splat steps after the
//     first attr (the schema models leaf attributes by name; mixed
//     paths like `aws_X.y.list_block[*].name` would need a richer
//     walker — for now we err on the side of "don't flag what we
//     can't cleanly verify").
//
// Designed to be called by the loader after AnalyseFiles. The
// errors flow through Module.Validate() alongside the existing
// undefined-reference findings, so `tflens validate` surfaces them
// without any cmd-side wiring beyond the loader integration.
func CheckResourceAttrRefs(m *Module, schema *providerschema.Schema) {
	if m == nil || schema == nil {
		return
	}
	for _, e := range m.entities {
		entityID := e.ID()
		for _, expr := range collectEntityExprs(e) {
			if expr == nil || expr.E == nil {
				continue
			}
			checkExprResourceRefs(m, schema, entityID, expr.E)
		}
	}
}

// collectEntityExprs returns every *Expr stored on the entity in a
// flat slice — the expressions whose references we want to validate.
// Recurses into nested body blocks and dynamic blocks so deeply
// nested attribute references inside content bodies are still
// checked.
func collectEntityExprs(e Entity) []*Expr {
	out := []*Expr{
		e.DefaultExpr, e.ValueExpr, e.ProviderExpr,
		e.CountExpr, e.ForEachExpr, e.DependsOnExpr,
		e.IgnoreChangesExpr, e.ReplaceTriggeredByExpr,
		e.LocalExpr,
	}
	for _, x := range e.ModuleArgs {
		out = append(out, x)
	}
	for _, x := range e.BodyAttrs {
		out = append(out, x)
	}
	for _, instances := range e.BodyBlocks {
		for _, b := range instances {
			out = append(out, collectBodyBlockExprs(b)...)
		}
	}
	for _, instances := range e.BodyDynamicBlocks {
		for _, d := range instances {
			out = append(out, collectDynamicBlockExprs(d)...)
		}
	}
	for _, c := range e.Validations {
		out = append(out, c.Condition, c.ErrorMessage)
	}
	for _, c := range e.Preconditions {
		out = append(out, c.Condition, c.ErrorMessage)
	}
	for _, c := range e.Postconditions {
		out = append(out, c.Condition, c.ErrorMessage)
	}
	return out
}

func collectBodyBlockExprs(b *BodyBlock) []*Expr {
	if b == nil {
		return nil
	}
	out := make([]*Expr, 0, len(b.Attrs))
	for _, x := range b.Attrs {
		out = append(out, x)
	}
	for _, instances := range b.Blocks {
		for _, sub := range instances {
			out = append(out, collectBodyBlockExprs(sub)...)
		}
	}
	for _, instances := range b.DynamicBlocks {
		for _, d := range instances {
			out = append(out, collectDynamicBlockExprs(d)...)
		}
	}
	return out
}

func collectDynamicBlockExprs(d *BodyDynamicBlock) []*Expr {
	if d == nil {
		return nil
	}
	out := []*Expr{d.ForEach}
	out = append(out, collectBodyBlockExprs(d.Content)...)
	return out
}

// checkExprResourceRefs inspects every traversal in the expression
// and emits a ValidationError for each resource attribute reference
// whose path doesn't resolve in the schema. See CheckResourceAttrRefs
// for the false-positive guards.
func checkExprResourceRefs(m *Module, schema *providerschema.Schema, entityID string, expr hclsyntax.Expression) {
	for _, trav := range expr.Variables() {
		if len(trav) < 3 {
			continue
		}
		root, ok := trav[0].(hcl.TraverseRoot)
		if !ok {
			continue
		}
		switch root.Name {
		case "var", "local", "module":
			continue
		case "data":
			checkDataResourceRef(m, schema, entityID, trav)
			continue
		}
		nameStep, ok := trav[1].(hcl.TraverseAttr)
		if !ok {
			continue
		}
		resType := root.Name
		if !m.HasEntity((Entity{Kind: KindResource, Type: resType, Name: nameStep.Name}).ID()) {
			continue
		}
		if !schema.HasResource(resType) {
			continue
		}
		path, indeterminate := schemaCheckablePath(trav[2:])
		if indeterminate || len(path) == 0 {
			continue
		}
		if schema.HasAttribute(resType, path) {
			continue
		}
		m.valErrs = append(m.valErrs, ValidationError{
			EntityID: entityID,
			Ref:      resType + "." + nameStep.Name + "." + strings.Join(path, "."),
			Pos:      posFromRange(trav.SourceRange()),
			Msg: fmt.Sprintf("attribute %q does not exist on resource type %s (referenced by %s)",
				strings.Join(path, "."), resType, entityID),
		})
	}
}

// checkDataResourceRef is the data-source counterpart of the resource
// branch above. Path layout is `data.<type>.<name>(.<path>...)` so
// the type is at trav[1] and the name at trav[2].
func checkDataResourceRef(m *Module, schema *providerschema.Schema, entityID string, trav hcl.Traversal) {
	if len(trav) < 4 {
		return
	}
	typeStep, ok := trav[1].(hcl.TraverseAttr)
	if !ok {
		return
	}
	nameStep, ok := trav[2].(hcl.TraverseAttr)
	if !ok {
		return
	}
	dataType := typeStep.Name
	if !m.HasEntity((Entity{Kind: KindData, Type: dataType, Name: nameStep.Name}).ID()) {
		return
	}
	if !schema.HasDataSource(dataType) {
		return
	}
	path, indeterminate := schemaCheckablePath(trav[3:])
	if indeterminate || len(path) == 0 {
		return
	}
	if schema.HasDataAttribute(dataType, path) {
		return
	}
	m.valErrs = append(m.valErrs, ValidationError{
		EntityID: entityID,
		Ref:      "data." + dataType + "." + nameStep.Name + "." + strings.Join(path, "."),
		Pos:      posFromRange(trav.SourceRange()),
		Msg: fmt.Sprintf("attribute %q does not exist on data source type %s (referenced by %s)",
			strings.Join(path, "."), dataType, entityID),
	})
}

// schemaCheckablePath strips leading instance selectors (count /
// for_each `[k]` / splat `[*]`) and then collects the leading
// contiguous run of attribute steps as a string slice. Returns
// indeterminate=true when the post-selector traversal contains an
// index or splat step interleaved with attrs (e.g. accessing a
// list-typed nested block via `aws_X.y.list_block[*].name`) — those
// shapes need a richer walker than HasAttribute provides, so we
// skip rather than risk a false positive.
func schemaCheckablePath(steps []hcl.Traverser) (path []string, indeterminate bool) {
	i := 0
	for i < len(steps) {
		switch steps[i].(type) {
		case hcl.TraverseAttr:
			break
		case hcl.TraverseIndex, hcl.TraverseSplat:
			i++
			continue
		default:
			return nil, true
		}
		break
	}
	for ; i < len(steps); i++ {
		attr, ok := steps[i].(hcl.TraverseAttr)
		if !ok {
			// A non-attr step interleaved with attrs — schema lookup
			// can't cleanly verify the rest. Conservative: skip.
			return path, true
		}
		path = append(path, attr.Name)
	}
	return path, false
}
