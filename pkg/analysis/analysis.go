// Package analysis builds an entity inventory and dependency graph from one
// or more parsed Terraform files. It operates on hashicorp/hcl/v2's
// hclsyntax trees with no network or filesystem access.
package analysis

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"
	"github.com/zclconf/go-cty/cty"

	"github.com/dgr237/tflens/pkg/token"
)

// EntityKind classifies a top-level Terraform entity.
type EntityKind string

const (
	KindResource EntityKind = "resource"
	KindData     EntityKind = "data"
	KindVariable EntityKind = "variable"
	KindLocal    EntityKind = "local"
	KindOutput   EntityKind = "output"
	KindModule   EntityKind = "module"
)

// Expr wraps an hclsyntax.Expression together with the source bytes of the
// file that contains it. Keeping the source lets downstream consumers
// (notably pkg/diff) compute a deterministic canonical text form without
// re-parsing, while the hclsyntax.Expression itself remains available for
// structural walks (variable references, type inference).
type Expr struct {
	E      hclsyntax.Expression
	Source []byte
}

// Text returns a canonical text form of the expression, suitable as a
// comparison key. Runs hclwrite's formatter over the expression's source
// bytes so semantically-identical inputs with different whitespace produce
// the same key. Nil-safe: returns "" for a nil *Expr.
func (e *Expr) Text() string {
	if e == nil || e.E == nil {
		return ""
	}
	r := e.E.Range()
	if r.Start.Byte < 0 || r.End.Byte > len(e.Source) || r.Start.Byte > r.End.Byte {
		return ""
	}
	raw := e.Source[r.Start.Byte:r.End.Byte]
	return strings.TrimSpace(string(hclwrite.Format(raw)))
}

// Range returns the source range of the expression.
func (e *Expr) Range() hcl.Range {
	if e == nil || e.E == nil {
		return hcl.Range{}
	}
	return e.E.Range()
}

// Pos returns the start position of the expression in the legacy
// token.Position form.
func (e *Expr) Pos() token.Position {
	return posFromRange(e.Range())
}

// Entity is a single named thing declared in a Terraform module.
type Entity struct {
	Kind           EntityKind
	Type           string // non-empty for resource and data source
	Name           string
	Pos            token.Position   // source location of the declaration
	DeclaredType   *TFType          // parsed type constraint; non-nil only for variables
	HasDefault     bool             // variables: a default value was declared
	DefaultExpr    *Expr            // variables: the default value expression
	HasCount       bool             // resource/data/module: count meta-argument used
	HasForEach     bool             // resource/data/module: for_each meta-argument used
	NonNullable    bool             // variables: `nullable = false` explicitly set
	Sensitive      bool             // variables/outputs: `sensitive = true` set
	Ephemeral      bool             // variables/outputs: `ephemeral = true` (Terraform 1.10+)
	Validations    int              // variables: number of `validation {}` blocks
	Preconditions  int              // variables/outputs/resources: number of precondition blocks
	Postconditions int              // variables/outputs/resources: number of postcondition blocks

	// Canonical text of every block's `condition` attribute, in source
	// order. Used by pkg/diff to detect content changes that count
	// comparisons miss (e.g. one validation removed and another added with
	// a different condition — both leaving the count unchanged).
	ValidationConditions    []string
	PreconditionConditions  []string
	PostconditionConditions []string
	ValueExpr      *Expr            // outputs: the value expression
	ProviderExpr   *Expr            // resource/data: value of `provider` attribute
	ModuleArgs     map[string]*Expr // module blocks: argument-name → expression (excludes meta-args)
	LocalExpr      *Expr            // locals: the local's value expression
	ForEachExpr    *Expr            // resource/data/module: value of `for_each`
	CountExpr      *Expr            // resource/data/module: value of `count`
	DependsOnExpr  *Expr            // resource/data/module/output: value of `depends_on`

	// Lifecycle block (resources only)
	PreventDestroy         bool  // `prevent_destroy = true`
	CreateBeforeDestroy    bool  // `create_before_destroy = true`
	IgnoreChangesExpr      *Expr // value of `ignore_changes`
	ReplaceTriggeredByExpr *Expr // value of `replace_triggered_by`
}

// Location returns a short "file:line" string suitable for display.
func (e Entity) Location() string {
	if e.Pos.File == "" && e.Pos.Line == 0 {
		return ""
	}
	if e.Pos.File == "" {
		return fmt.Sprintf("%d", e.Pos.Line)
	}
	return fmt.Sprintf("%s:%d", filepath.Base(e.Pos.File), e.Pos.Line)
}

// ID returns the canonical string identifier used as graph node key.
func (e Entity) ID() string {
	switch e.Kind {
	case KindResource, KindData:
		return fmt.Sprintf("%s.%s.%s", e.Kind, e.Type, e.Name)
	default:
		return fmt.Sprintf("%s.%s", e.Kind, e.Name)
	}
}

func (e Entity) String() string { return e.ID() }

// ValidationError records a reference that points to an undeclared Terraform
// entity, or another validation failure.
type ValidationError struct {
	EntityID string         // entity that contains the reference
	Ref      string         // canonical ID of the missing entity, e.g. "variable.typo"
	Pos      token.Position // source location of the reference
	Msg      string         // if non-empty, replaces the default format in Error()
}

func (e ValidationError) Error() string {
	if e.Msg != "" {
		return fmt.Sprintf("%s: %s", e.Pos, e.Msg)
	}
	return fmt.Sprintf("%s: %s is undefined (referenced by %s)", e.Pos, e.Ref, e.EntityID)
}

// ProviderRequirement describes one entry inside required_providers {}.
type ProviderRequirement struct {
	Source  string
	Version string
}

// Module is the analysis result for one Terraform module (one directory).
type Module struct {
	entities          []Entity
	byID              map[string]Entity
	deps              map[string]map[string]bool
	moduleSources     map[string]string
	moduleVersions    map[string]string
	valErrs           []ValidationError
	typeErrs          []TypeCheckError
	moved             map[string]string
	removedIDs        map[string]bool
	requiredVersion   string
	requiredProviders map[string]ProviderRequirement
	backend           *Backend

	// moduleOutputRefs is the set of output names referenced via
	// module.<callName>.<outputName> traversals anywhere in this module's
	// expressions, keyed by call name. Populated during dep collection so
	// cross_validate can flag references whose target output no longer
	// exists in the new child.
	moduleOutputRefs map[string]map[string]bool

	// tracked is the set of attributes annotated with `# tflens:track`
	// markers, in source order across all files in this module.
	tracked []TrackedAttribute
}

// Backend describes the terraform { backend "X" { ... } } block.
// Type is the backend label (e.g. "s3", "azurerm", "remote"). Config is
// the canonical text of every attribute in the block, keyed by attribute
// name, so callers can compare configurations across versions without
// caring about formatting.
type Backend struct {
	Type   string
	Config map[string]string
	Pos    token.Position
}

func newModule() *Module {
	return &Module{
		byID:              make(map[string]Entity),
		deps:              make(map[string]map[string]bool),
		moduleSources:     make(map[string]string),
		moduleVersions:    make(map[string]string),
		moved:             make(map[string]string),
		removedIDs:        make(map[string]bool),
		requiredProviders: make(map[string]ProviderRequirement),
		moduleOutputRefs:  make(map[string]map[string]bool),
	}
}

// Backend returns the parsed terraform { backend ... } configuration, or
// nil when the module declares no backend block (i.e. uses the default
// local backend). Nil-safe.
func (m *Module) Backend() *Backend {
	if m == nil {
		return nil
	}
	return m.backend
}

// RequiredVersion returns the Terraform CLI version constraint declared by
// a terraform { required_version = ... } block, or an empty string if none.
// Nil-safe.
func (m *Module) RequiredVersion() string {
	if m == nil {
		return ""
	}
	return m.requiredVersion
}

// RequiredProviders returns a copy of the provider requirements declared in
// terraform { required_providers { ... } }. Nil-safe.
func (m *Module) RequiredProviders() map[string]ProviderRequirement {
	if m == nil {
		return map[string]ProviderRequirement{}
	}
	out := make(map[string]ProviderRequirement, len(m.requiredProviders))
	for k, v := range m.requiredProviders {
		out[k] = v
	}
	return out
}

// Moved returns the rename pairs declared by `moved` blocks in this module.
// Nil-safe.
func (m *Module) Moved() map[string]string {
	if m == nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(m.moved))
	for k, v := range m.moved {
		out[k] = v
	}
	return out
}

// RemovedDeclared reports whether id was declared by a `removed` block.
// Nil-safe.
func (m *Module) RemovedDeclared(id string) bool {
	if m == nil {
		return false
	}
	return m.removedIDs[id]
}

// Validate returns all undefined-reference errors found during analysis,
// sorted by source location. Nil-safe.
func (m *Module) Validate() []ValidationError {
	if m == nil {
		return nil
	}
	out := make([]ValidationError, len(m.valErrs))
	copy(out, m.valErrs)
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

// ModuleSource returns the value of the source attribute for a module call.
// Nil-safe.
func (m *Module) ModuleSource(name string) string {
	if m == nil {
		return ""
	}
	return m.moduleSources[name]
}

// ModuleVersion returns the version constraint for a module call. Nil-safe.
func (m *Module) ModuleVersion(name string) string {
	if m == nil {
		return ""
	}
	return m.moduleVersions[name]
}

// ModuleOutputReferences returns the sorted set of output names this
// module references via module.<callName>.<outputName> traversals (in
// outputs, locals, resource attributes, or anywhere else in the module's
// expressions). Returns an empty slice when callName has no recognised
// references — including the case where every reference was just bare
// `module.<callName>` with no .attribute suffix. Nil-safe.
func (m *Module) ModuleOutputReferences(callName string) []string {
	if m == nil {
		return nil
	}
	set := m.moduleOutputRefs[callName]
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// HasEntity reports whether an entity with the given canonical ID
// (e.g. "resource.aws_vpc.main", "variable.region") is declared in
// this module. Constant-time — uses the byID index built during
// analysis.
func (m *Module) HasEntity(id string) bool {
	if m == nil {
		return false
	}
	_, ok := m.byID[id]
	return ok
}

// EntityByID returns the entity with the given canonical ID and
// reports whether it was found. Constant-time. Convenience for
// callers that need both existence and the entity value.
func (m *Module) EntityByID(id string) (Entity, bool) {
	if m == nil {
		return Entity{}, false
	}
	e, ok := m.byID[id]
	return e, ok
}

func (m *Module) addEntity(e Entity) {
	if _, exists := m.byID[e.ID()]; !exists {
		m.entities = append(m.entities, e)
		m.byID[e.ID()] = e
	}
}

func (m *Module) addDep(from, to string) {
	if from == to {
		return
	}
	if _, ok := m.deps[from]; !ok {
		m.deps[from] = make(map[string]bool)
	}
	m.deps[from][to] = true
}

// Entities returns all declared entities in declaration order.
// Nil-safe: returns nil for a nil receiver.
func (m *Module) Entities() []Entity {
	if m == nil {
		return nil
	}
	return m.entities
}

// Filter returns all entities of the given kind, in declaration
// order. Nil-safe: returns nil for a nil receiver.
func (m *Module) Filter(kind EntityKind) []Entity {
	if m == nil {
		return nil
	}
	var out []Entity
	for _, e := range m.entities {
		if e.Kind == kind {
			out = append(out, e)
		}
	}
	return out
}

// Dependencies returns the sorted list of entity IDs that id directly
// depends on.
func (m *Module) Dependencies(id string) []string {
	set := m.deps[id]
	out := make([]string, 0, len(set))
	for dep := range set {
		out = append(out, dep)
	}
	sort.Strings(out)
	return out
}

// Dependents returns the sorted list of entity IDs that directly depend on id.
func (m *Module) Dependents(id string) []string {
	var out []string
	for from, toSet := range m.deps {
		if toSet[id] {
			out = append(out, from)
		}
	}
	sort.Strings(out)
	return out
}

// HasDep reports whether from directly depends on to.
func (m *Module) HasDep(from, to string) bool { return m.deps[from][to] }

// ToDOT returns the dependency graph in Graphviz DOT format.
func (m *Module) ToDOT() string {
	var sb strings.Builder
	sb.WriteString("digraph terraform {\n")
	sb.WriteString("  rankdir=LR;\n")
	sb.WriteString("  node [shape=box fontname=monospace];\n\n")

	kindColour := map[EntityKind]string{
		KindResource: "#aed6f1",
		KindData:     "#a9dfbf",
		KindVariable: "#f9e79f",
		KindLocal:    "#f5cba7",
		KindOutput:   "#d7bde2",
		KindModule:   "#fadbd8",
	}
	for _, e := range m.entities {
		colour := kindColour[e.Kind]
		sb.WriteString(fmt.Sprintf("  %q [label=%q style=filled fillcolor=%q];\n",
			e.ID(), e.ID(), colour))
	}
	sb.WriteByte('\n')

	froms := make([]string, 0, len(m.deps))
	for from := range m.deps {
		froms = append(froms, from)
	}
	sort.Strings(froms)
	for _, from := range froms {
		tos := m.Dependencies(from)
		for _, to := range tos {
			sb.WriteString(fmt.Sprintf("  %q -> %q;\n", from, to))
		}
	}
	sb.WriteString("}\n")
	return sb.String()
}

// ---- analysis entry points ----

// File is one parsed Terraform source file: the hclsyntax body plus the
// source bytes the parser was given. The source is needed for Expr.Text().
type File struct {
	Filename string
	Source   []byte
	Body     *hclsyntax.Body
}

// Analyse runs the full pipeline on a single file.
func Analyse(file *File) *Module { return AnalyseFiles([]*File{file}) }

// AnalyseFiles merges multiple parsed files into a single module analysis.
// All entity declarations are collected before any dependency edges are
// resolved, so cross-file references work correctly.
func AnalyseFiles(files []*File) *Module {
	m := newModule()
	for _, f := range files {
		collectEntities(m, f)
	}
	for _, f := range files {
		collectDependencies(m, f)
	}
	for _, f := range files {
		typeCheckBodies(m, f)
	}
	for _, f := range files {
		m.tracked = append(m.tracked, collectTrackedAttributes(f)...)
	}
	m.resolveTrackedRefs()
	checkSensitivePropagation(m)
	return m
}

// checkSensitivePropagation emits a ValidationError for every output whose
// value expression references a sensitive variable without being marked
// sensitive itself. Mirrors the error Terraform itself produces at plan time.
func checkSensitivePropagation(m *Module) {
	for _, e := range m.entities {
		if e.Kind != KindOutput || e.Sensitive || e.ValueExpr == nil || e.ValueExpr.E == nil {
			continue
		}
		seen := make(map[string]bool)
		for _, trav := range e.ValueExpr.E.Variables() {
			parts := traversalParts(trav)
			if len(parts) < 2 || parts[0] != "var" {
				continue
			}
			varID := (Entity{Kind: KindVariable, Name: parts[1]}).ID()
			if seen[varID] {
				continue
			}
			v, ok := m.byID[varID]
			if !ok || !v.Sensitive {
				continue
			}
			seen[varID] = true
			m.valErrs = append(m.valErrs, ValidationError{
				EntityID: e.ID(),
				Ref:      varID,
				Pos:      posFromRange(trav.SourceRange()),
				Msg: fmt.Sprintf("%s references sensitive %s but is not itself marked sensitive (Terraform will reject the plan)",
					e.ID(), varID),
			})
		}
	}
}

// collectEntities performs a first pass to register every declared entity.
func collectEntities(m *Module, file *File) {
	for _, block := range file.Body.Blocks {
		switch block.Type {
		case "resource":
			if len(block.Labels) == 2 {
				e := Entity{Kind: KindResource, Type: block.Labels[0], Name: block.Labels[1], Pos: posFromRange(block.DefRange())}
				scanMetaArgs(&e, block.Body, file.Source)
				m.addEntity(e)
			}
		case "data":
			if len(block.Labels) == 2 {
				e := Entity{Kind: KindData, Type: block.Labels[0], Name: block.Labels[1], Pos: posFromRange(block.DefRange())}
				scanMetaArgs(&e, block.Body, file.Source)
				m.addEntity(e)
			}
		case "variable":
			if len(block.Labels) == 1 {
				e, te := variableEntity(block, file.Source)
				m.addEntity(e)
				if te != nil {
					m.typeErrs = append(m.typeErrs, *te)
				}
			}
		case "output":
			if len(block.Labels) == 1 {
				e := outputEntity(block, file.Source)
				m.addEntity(e)
			}
		case "module":
			if len(block.Labels) == 1 {
				name := block.Labels[0]
				e := Entity{Kind: KindModule, Name: name, Pos: posFromRange(block.DefRange()), ModuleArgs: map[string]*Expr{}}
				scanMetaArgs(&e, block.Body, file.Source)
				for _, attr := range sortedAttrs(block.Body) {
					if isModuleMetaArg(attr.Name) {
						if s, ok := constantString(attr.Expr); ok {
							switch attr.Name {
							case "source":
								m.moduleSources[name] = s
							case "version":
								m.moduleVersions[name] = s
							}
						}
						continue
					}
					e.ModuleArgs[attr.Name] = &Expr{E: attr.Expr, Source: file.Source}
				}
				m.addEntity(e)
			}
		case "locals":
			for _, attr := range sortedAttrs(block.Body) {
				m.addEntity(Entity{
					Kind:      KindLocal,
					Name:      attr.Name,
					Pos:       posFromRange(attr.NameRange),
					LocalExpr: &Expr{E: attr.Expr, Source: file.Source},
				})
			}
		case "moved":
			from, to := extractFromTo(block.Body)
			if from != "" && to != "" {
				m.moved[from] = to
			}
		case "removed":
			from, _ := extractFromTo(block.Body)
			if from != "" {
				m.removedIDs[from] = true
			}
		case "terraform":
			scanTerraformBlock(m, block.Body, file.Source)
		}
	}
}

// variableEntity builds an Entity for a variable block. If both a type
// constraint and a default are declared, also checks that the default is
// statically convertible to the declared type and returns a TypeCheckError
// on failure.
func variableEntity(block *hclsyntax.Block, src []byte) (Entity, *TypeCheckError) {
	e := Entity{
		Kind: KindVariable,
		Name: block.Labels[0],
		Pos:  posFromRange(block.DefRange()),
	}
	var typeExpr hclsyntax.Expression
	for _, attr := range sortedAttrs(block.Body) {
		switch attr.Name {
		case "type":
			typeExpr = attr.Expr
			e.DeclaredType = ParseTypeExpr(attr.Expr)
		case "default":
			e.HasDefault = true
			e.DefaultExpr = &Expr{E: attr.Expr, Source: src}
		case "nullable":
			if b, ok := constantBool(attr.Expr); ok && !b {
				e.NonNullable = true
			}
		case "sensitive":
			if b, ok := constantBool(attr.Expr); ok && b {
				e.Sensitive = true
			}
		case "ephemeral":
			if b, ok := constantBool(attr.Expr); ok && b {
				e.Ephemeral = true
			}
		}
	}
	for _, b := range block.Body.Blocks {
		switch b.Type {
		case "validation":
			e.Validations++
			e.ValidationConditions = append(e.ValidationConditions, blockCondition(b, src))
		case "precondition":
			e.Preconditions++
			e.PreconditionConditions = append(e.PreconditionConditions, blockCondition(b, src))
		case "postcondition":
			e.Postconditions++
			e.PostconditionConditions = append(e.PostconditionConditions, blockCondition(b, src))
		}
	}

	var te *TypeCheckError
	if typeExpr != nil && e.DefaultExpr != nil {
		te = checkDefaultConvertible(e, typeExpr)
	}
	return e, te
}

// outputEntity builds an Entity for an output block.
func outputEntity(block *hclsyntax.Block, src []byte) Entity {
	e := Entity{
		Kind: KindOutput,
		Name: block.Labels[0],
		Pos:  posFromRange(block.DefRange()),
	}
	for _, attr := range sortedAttrs(block.Body) {
		switch attr.Name {
		case "sensitive":
			if b, ok := constantBool(attr.Expr); ok && b {
				e.Sensitive = true
			}
		case "ephemeral":
			if b, ok := constantBool(attr.Expr); ok && b {
				e.Ephemeral = true
			}
		case "value":
			e.ValueExpr = &Expr{E: attr.Expr, Source: src}
		case "depends_on":
			e.DependsOnExpr = &Expr{E: attr.Expr, Source: src}
		}
	}
	for _, b := range block.Body.Blocks {
		switch b.Type {
		case "precondition":
			e.Preconditions++
			e.PreconditionConditions = append(e.PreconditionConditions, blockCondition(b, src))
		case "postcondition":
			e.Postconditions++
			e.PostconditionConditions = append(e.PostconditionConditions, blockCondition(b, src))
		}
	}
	return e
}

// blockCondition returns the canonical text of a validation/precondition/
// postcondition block's `condition` attribute, or "" when the block has no
// condition attribute.
func blockCondition(b *hclsyntax.Block, src []byte) string {
	if b.Body == nil {
		return ""
	}
	attr, ok := b.Body.Attributes["condition"]
	if !ok {
		return ""
	}
	return (&Expr{E: attr.Expr, Source: src}).Text()
}

// scanTerraformBlock reads the attributes and sub-blocks of a top-level
// terraform {} block into the Module's requiredVersion, requiredProviders,
// and backend fields.
func scanTerraformBlock(m *Module, body *hclsyntax.Body, src []byte) {
	for name, attr := range body.Attributes {
		if name == "required_version" {
			if s, ok := constantString(attr.Expr); ok {
				m.requiredVersion = s
			}
		}
	}
	for _, child := range body.Blocks {
		switch child.Type {
		case "required_providers":
			for _, attr := range sortedAttrs(child.Body) {
				obj, ok := attr.Expr.(*hclsyntax.ObjectConsExpr)
				if !ok {
					continue
				}
				var req ProviderRequirement
				for _, item := range obj.Items {
					key := objectKeyName(item.KeyExpr)
					s, ok := constantString(item.ValueExpr)
					if !ok {
						continue
					}
					switch key {
					case "source":
						req.Source = s
					case "version":
						req.Version = s
					}
				}
				m.requiredProviders[attr.Name] = req
			}
		case "backend":
			if len(child.Labels) != 1 || m.backend != nil {
				continue
			}
			b := &Backend{
				Type:   child.Labels[0],
				Config: make(map[string]string),
				Pos:    posFromRange(child.DefRange()),
			}
			for _, attr := range sortedAttrs(child.Body) {
				e := &Expr{E: attr.Expr, Source: src}
				b.Config[attr.Name] = e.Text()
			}
			m.backend = b
		}
	}
}

// extractFromTo reads the `from` and `to` attributes of a moved/removed block
// body and returns them as canonical entity IDs.
func extractFromTo(body *hclsyntax.Body) (from, to string) {
	for name, attr := range body.Attributes {
		stv, ok := attr.Expr.(*hclsyntax.ScopeTraversalExpr)
		if !ok {
			continue
		}
		parts := traversalParts(stv.Traversal)
		switch name {
		case "from":
			from = refPartsToEntityID(parts)
		case "to":
			to = refPartsToEntityID(parts)
		}
	}
	return from, to
}

// refPartsToEntityID converts a reference's parts to a canonical entity ID.
func refPartsToEntityID(parts []string) string {
	if len(parts) < 2 {
		return ""
	}
	switch parts[0] {
	case "module":
		return (Entity{Kind: KindModule, Name: parts[1]}).ID()
	case "data":
		if len(parts) < 3 {
			return ""
		}
		return (Entity{Kind: KindData, Type: parts[1], Name: parts[2]}).ID()
	}
	return (Entity{Kind: KindResource, Type: parts[0], Name: parts[1]}).ID()
}

// isModuleMetaArg reports whether a named attribute is a Terraform-reserved
// meta-argument on a module block rather than an argument to the child module.
func isModuleMetaArg(name string) bool {
	switch name {
	case "source", "version", "count", "for_each", "providers", "depends_on", "provider":
		return true
	}
	return false
}

// scanMetaArgs sets meta-argument and lifecycle fields on e by inspecting the
// direct children of a resource/data/module block body.
func scanMetaArgs(e *Entity, body *hclsyntax.Body, src []byte) {
	for _, attr := range sortedAttrs(body) {
		switch attr.Name {
		case "count":
			e.HasCount = true
			e.CountExpr = &Expr{E: attr.Expr, Source: src}
		case "for_each":
			e.HasForEach = true
			e.ForEachExpr = &Expr{E: attr.Expr, Source: src}
		case "provider":
			e.ProviderExpr = &Expr{E: attr.Expr, Source: src}
		case "depends_on":
			e.DependsOnExpr = &Expr{E: attr.Expr, Source: src}
		}
	}
	for _, child := range body.Blocks {
		if child.Type == "lifecycle" {
			scanLifecycleBlock(e, child.Body, src)
		}
	}
}

// scanLifecycleBlock populates the lifecycle fields of e from the body of a
// `lifecycle {}` block.
func scanLifecycleBlock(e *Entity, body *hclsyntax.Body, src []byte) {
	for _, attr := range sortedAttrs(body) {
		switch attr.Name {
		case "prevent_destroy":
			if b, ok := constantBool(attr.Expr); ok {
				e.PreventDestroy = b
			}
		case "create_before_destroy":
			if b, ok := constantBool(attr.Expr); ok {
				e.CreateBeforeDestroy = b
			}
		case "ignore_changes":
			e.IgnoreChangesExpr = &Expr{E: attr.Expr, Source: src}
		case "replace_triggered_by":
			e.ReplaceTriggeredByExpr = &Expr{E: attr.Expr, Source: src}
		}
	}
	for _, child := range body.Blocks {
		switch child.Type {
		case "precondition":
			e.Preconditions++
			e.PreconditionConditions = append(e.PreconditionConditions, blockCondition(child, src))
		case "postcondition":
			e.Postconditions++
			e.PostconditionConditions = append(e.PostconditionConditions, blockCondition(child, src))
		}
	}
}

// collectDependencies performs a second pass to build dependency edges.
// Runs after collectEntities so resource references can be distinguished from
// unrelated identifiers by checking m.byID.
func collectDependencies(m *Module, file *File) {
	for _, block := range file.Body.Blocks {
		switch block.Type {
		case "resource":
			if len(block.Labels) == 2 {
				id := (Entity{Kind: KindResource, Type: block.Labels[0], Name: block.Labels[1]}).ID()
				m.walkBody(id, block.Body)
			}
		case "data":
			if len(block.Labels) == 2 {
				id := (Entity{Kind: KindData, Type: block.Labels[0], Name: block.Labels[1]}).ID()
				m.walkBody(id, block.Body)
			}
		case "output":
			if len(block.Labels) == 1 {
				id := (Entity{Kind: KindOutput, Name: block.Labels[0]}).ID()
				m.walkBody(id, block.Body)
			}
		case "module":
			if len(block.Labels) == 1 {
				id := (Entity{Kind: KindModule, Name: block.Labels[0]}).ID()
				m.walkBody(id, block.Body)
			}
		case "locals":
			for _, attr := range sortedAttrs(block.Body) {
				id := (Entity{Kind: KindLocal, Name: attr.Name}).ID()
				m.walkExpr(id, attr.Expr)
			}
		}
	}
}

// walkBody recurses through a body, calling walkExpr on every attribute
// expression. Nested blocks are descended into but references are still
// attributed to fromID.
func (m *Module) walkBody(fromID string, body *hclsyntax.Body) {
	for _, attr := range body.Attributes {
		m.walkExpr(fromID, attr.Expr)
	}
	for _, b := range body.Blocks {
		m.walkBody(fromID, b.Body)
	}
}

// walkExpr collects every traversal reachable from expr and records it as a
// dep edge (known target) or a ValidationError (undefined target).
func (m *Module) walkExpr(fromID string, expr hclsyntax.Expression) {
	for _, trav := range expr.Variables() {
		parts := traversalParts(trav)
		m.recordRef(fromID, parts, posFromRange(trav.SourceRange()))
	}
}

// recordRef resolves one reference: adds a dep edge if the target is known,
// or a ValidationError if the prefix is a user-space keyword that must
// resolve to a declared entity. Bare resource-style refs are not validated
// to avoid false positives from for-expression iteration variables.
func (m *Module) recordRef(fromID string, parts []string, pos token.Position) {
	if dep, ok := m.classifyRef(parts); ok {
		m.addDep(fromID, dep.ID())
		// Track which outputs of each module call are referenced so
		// cross_validate can detect when a removed output broke a
		// caller. parts is e.g. ["module", "vpc", "subnet_ids"]; we want
		// "subnet_ids" recorded against "vpc".
		if dep.Kind == KindModule && len(parts) >= 3 {
			set, ok := m.moduleOutputRefs[parts[1]]
			if !ok {
				set = make(map[string]bool)
				m.moduleOutputRefs[parts[1]] = set
			}
			set[parts[2]] = true
		}
		return
	}
	if len(parts) < 2 {
		return
	}
	var missing string
	switch parts[0] {
	case "var":
		missing = (Entity{Kind: KindVariable, Name: parts[1]}).ID()
	case "local":
		missing = (Entity{Kind: KindLocal, Name: parts[1]}).ID()
	case "module":
		missing = (Entity{Kind: KindModule, Name: parts[1]}).ID()
	case "data":
		if len(parts) >= 3 {
			missing = (Entity{Kind: KindData, Type: parts[1], Name: parts[2]}).ID()
		}
	}
	if missing != "" {
		m.valErrs = append(m.valErrs, ValidationError{
			EntityID: fromID,
			Ref:      missing,
			Pos:      pos,
		})
	}
}

// classifyRef interprets a reference's parts as a Terraform entity reference.
func (m *Module) classifyRef(parts []string) (Entity, bool) {
	if len(parts) < 2 {
		return Entity{}, false
	}
	switch parts[0] {
	case "var":
		e := Entity{Kind: KindVariable, Name: parts[1]}
		if _, ok := m.byID[e.ID()]; ok {
			return e, true
		}
	case "local":
		e := Entity{Kind: KindLocal, Name: parts[1]}
		if _, ok := m.byID[e.ID()]; ok {
			return e, true
		}
	case "module":
		e := Entity{Kind: KindModule, Name: parts[1]}
		if _, ok := m.byID[e.ID()]; ok {
			return e, true
		}
	case "data":
		if len(parts) >= 3 {
			e := Entity{Kind: KindData, Type: parts[1], Name: parts[2]}
			if _, ok := m.byID[e.ID()]; ok {
				return e, true
			}
		}
	default:
		// Potential resource reference: type.name
		e := Entity{Kind: KindResource, Type: parts[0], Name: parts[1]}
		if _, ok := m.byID[e.ID()]; ok {
			return e, true
		}
	}
	return Entity{}, false
}

// ---- shared helpers ----

// traversalParts flattens a hcl.Traversal into the flat []string form used
// by entity classification. Only the root + any leading TraverseAttr steps
// are collected; the first index/splat ends the chain.
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

// sortedAttrs returns the attributes of body in source order. hclsyntax
// stores attributes in an unordered map, so we sort by the name-range byte
// offset to recover declaration order.
func sortedAttrs(body *hclsyntax.Body) []*hclsyntax.Attribute {
	attrs := make([]*hclsyntax.Attribute, 0, len(body.Attributes))
	for _, a := range body.Attributes {
		attrs = append(attrs, a)
	}
	sort.Slice(attrs, func(i, j int) bool {
		return attrs[i].SrcRange.Start.Byte < attrs[j].SrcRange.Start.Byte
	})
	return attrs
}

// objectKeyName extracts a string field name from an object literal key
// expression. Keys may be bare identifiers or string literals.
func objectKeyName(expr hclsyntax.Expression) string {
	// hclsyntax wraps bare-identifier object keys in ObjectConsKeyExpr.
	if oke, ok := expr.(*hclsyntax.ObjectConsKeyExpr); ok {
		if trav, diags := hcl.AbsTraversalForExpr(oke); !diags.HasErrors() && len(trav) == 1 {
			if r, ok := trav[0].(hcl.TraverseRoot); ok {
				return r.Name
			}
		}
		expr = oke.Wrapped
	}
	if s, ok := constantString(expr); ok {
		return s
	}
	return ""
}

// constantString returns the string value of expr if it is a static literal,
// otherwise ("", false).
func constantString(expr hclsyntax.Expression) (string, bool) {
	v, diags := expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || !v.Type().Equals(cty.String) {
		return "", false
	}
	return v.AsString(), true
}

// constantBool returns the bool value of expr if it is a static bool literal,
// otherwise (false, false).
func constantBool(expr hclsyntax.Expression) (bool, bool) {
	v, diags := expr.Value(nil)
	if diags.HasErrors() || v.IsNull() || !v.Type().Equals(cty.Bool) {
		return false, false
	}
	return v.True(), true
}

// posFromRange converts a hcl.Range to the legacy token.Position form.
func posFromRange(r hcl.Range) token.Position {
	return token.Position{
		File:   r.Filename,
		Line:   r.Start.Line,
		Column: r.Start.Column,
	}
}
