package render

import (
	"encoding/json"
	"io"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/hashicorp/hcl/v2"
	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/loader"
)

// ---- Experimental: export schema ----
//
// PROTOTYPE — shape is subject to change. The export emits the
// enriched entity model that tflens has already built up internally
// (entity types, defaults, evaluated values where statically
// resolvable, dependency graph, tracked-attribute markers) in a form
// suitable for downstream converters (kro, crossplane, Pulumi, etc.).
//
// Not yet emitted (deferred until a converter author tells us they
// need it): per-attribute resource bodies (only meta-args today),
// nested validation block contents, dynamic-block bodies, provider
// alias graph.
//
// The top-level _experimental flag and schema_version are explicit
// so consumers can detect breakage without guessing.

// 0.4.0-prototype closes the last two "still deferred" items from
// the prototype's documented gap list:
//
//   - terraform.providers: top-level `provider "X" { alias = "...",
//     ... }` blocks now appear under terraform.providers (distinct
//     from required_providers, which is the version-constraint side).
//     Each entry carries Type, Alias?, and the per-attribute Config
//     map. Resources point at non-default instances via the existing
//     `provider` meta-arg → `provider = aws.east` matches the entry
//     with Type=aws + Alias=east.
//
//   - validation/precondition/postcondition body contents:
//     ExportVariable.Validations, ExportOutput.Preconditions /
//     Postconditions, ExportResource.Preconditions / Postconditions
//     each become a list of {condition, error_message?} entries
//     (each is a full {text, value?, ast?} expression triple).
//     Replaces the previous count + condition-text-list shape that
//     diff used internally for change detection.
//
// 0.3.0-prototype added dynamic_blocks: a recursive
// `{<name>: [{for_each, iterator?, content}, ...]}` map alongside
// the static blocks map on every resource/data body and on every
// nested block. Pure addition (no field renames) but the schema is
// bumped so consumers can detect the new surface.
//
// 0.2.0-prototype unified every expression-bearing field (count,
// for_each, depends_on, lifecycle, module-call arguments, locals,
// outputs, variable defaults, …) under the same {text, value?, ast?}
// shape that resource attributes use, so consumers see one shape
// wherever an HCL expression appears in the export. Schema bumped
// from 0.1.0-prototype because the *_text string fields were
// removed in favour of nested objects.
const ExportSchemaVersion = "0.4.0-prototype"

type Export struct {
	Experimental  bool       `json:"_experimental"`
	Warning       string     `json:"_warning"`
	SchemaVersion string     `json:"schema_version"`
	TflensVersion string     `json:"tflens_version,omitempty"`
	Root          ExportNode `json:"root"`
}

type ExportNode struct {
	Dir      string                `json:"dir"`
	Source   string                `json:"source,omitempty"`  // for child modules: the original module-call source string
	Version  string                `json:"version,omitempty"` // for child modules
	Module   ExportModule          `json:"module"`
	Children map[string]ExportNode `json:"children,omitempty"`
}

type ExportModule struct {
	Variables    []ExportVariable    `json:"variables"`
	Outputs      []ExportOutput      `json:"outputs"`
	Resources    []ExportResource    `json:"resources"`
	DataSources  []ExportResource    `json:"data_sources"`
	Locals       []ExportLocal       `json:"locals"`
	ModuleCalls  []ExportModuleCall  `json:"module_calls"`
	Terraform    ExportTerraform     `json:"terraform"`
	Tracked      []ExportTracked     `json:"tracked_attributes"`
	Dependencies map[string][]string `json:"dependencies"`
}

type ExportVariable struct {
	Name        string                 `json:"name"`
	Type        string                 `json:"type,omitempty"`
	HasDefault  bool                   `json:"has_default"`
	Default     *ExportExpression      `json:"default,omitempty"`
	Sensitive   bool                   `json:"sensitive,omitempty"`
	Ephemeral   bool                   `json:"ephemeral,omitempty"`
	Nullable    bool                   `json:"nullable"`
	Validations []ExportConditionBlock `json:"validations,omitempty"`
	Location    string                 `json:"location,omitempty"`
}

type ExportOutput struct {
	Name           string                 `json:"name"`
	Value          *ExportExpression      `json:"value,omitempty"`
	Sensitive      bool                   `json:"sensitive,omitempty"`
	Ephemeral      bool                   `json:"ephemeral,omitempty"`
	DependsOn      *ExportExpression      `json:"depends_on,omitempty"`
	Preconditions  []ExportConditionBlock `json:"preconditions,omitempty"`
	Postconditions []ExportConditionBlock `json:"postconditions,omitempty"`
	Location       string                 `json:"location,omitempty"`
}

// ExportConditionBlock is one validation / precondition / postcondition
// block surfaced under the entity that declared it. Both Condition and
// ErrorMessage use the standard {text, value?, ast?} shape so consumers
// (converters, generators, schema validators) can decide whether to
// translate the boolean check into the target's native validation
// primitive or keep the source text.
type ExportConditionBlock struct {
	Condition    *ExportExpression `json:"condition,omitempty"`
	ErrorMessage *ExportExpression `json:"error_message,omitempty"`
	Location     string            `json:"location,omitempty"`
}

type ExportResource struct {
	Type                string                          `json:"type"`
	Name                string                          `json:"name"`
	Provider            *ExportExpression               `json:"provider,omitempty"`
	Count               *ExportExpression               `json:"count,omitempty"`
	ForEach             *ExportExpression               `json:"for_each,omitempty"`
	DependsOn           *ExportExpression               `json:"depends_on,omitempty"`
	PreventDestroy      bool                            `json:"prevent_destroy,omitempty"`
	CreateBeforeDestroy bool                            `json:"create_before_destroy,omitempty"`
	IgnoreChanges       *ExportExpression               `json:"ignore_changes,omitempty"`
	ReplaceTriggeredBy  *ExportExpression               `json:"replace_triggered_by,omitempty"`
	Attributes          map[string]ExportExpression     `json:"attributes,omitempty"`
	Blocks              map[string][]ExportBlock        `json:"blocks,omitempty"`
	Preconditions       []ExportConditionBlock          `json:"preconditions,omitempty"`
	Postconditions      []ExportConditionBlock          `json:"postconditions,omitempty"`
	DynamicBlocks       map[string][]ExportDynamicBlock `json:"dynamic_blocks,omitempty"`
	Location            string                          `json:"location,omitempty"`
	// Note: `lifecycle` is excluded from Blocks because its meta-arg
	// attributes are projected into dedicated parent-resource fields
	// (PreventDestroy / IgnoreChanges / …). `dynamic` blocks are
	// surfaced separately in DynamicBlocks rather than Blocks because
	// their semantics are different — they generate the named block
	// at plan time from a for_each iteration over a content template.
}

// ExportExpression is the shared shape for every HCL expression that
// appears in the export — resource attributes, count/for_each/
// depends_on, lifecycle ignore_changes / replace_triggered_by,
// module-call arguments, locals, outputs, variable defaults. Three
// complementary fields:
//
//   - text:  the source as written. Always present. Lossless for
//     consumers that just want to pass the expression through.
//   - value: the evaluated cty value when the curated stdlib resolves
//     the expression. Lets converters emit literal values directly
//     for the common case of literal-heavy expressions
//     (`tags = { Name = "web" }`, `count = 3`, …).
//   - ast:   the structural decomposition (function calls, traversals,
//     conditionals, …). Lets converters translate expressions into
//     the target language without re-parsing the text. Especially
//     valuable for non-Go consumers that don't want to embed an HCL
//     parser. See export_ast.go for the supported node kinds.
//
// Renamed from ExportAttribute in 0.2.0-prototype: same shape, but
// "expression" describes its scope better now that count/for_each/
// arguments/locals/etc. all use it too.
type ExportExpression struct {
	Text  string         `json:"text"`
	Value *ExportCtyJSON `json:"value,omitempty"`
	AST   any            `json:"ast,omitempty"`
}

// ExportBlock is one nested block instance — recursive, since blocks
// can themselves contain blocks (e.g. aws_eks_cluster's
// `encryption_config { provider { key_arn = ... } }`). Repeated
// blocks (`ebs_block_device { ... }` × N) appear as multiple entries
// in the parent's []ExportBlock slice, in source order.
//
// DynamicBlocks captures `dynamic "<name>" { ... }` nested inside
// this block, so dynamic-inside-static (or dynamic-inside-dynamic
// via Content) recurses uniformly.
type ExportBlock struct {
	Attributes    map[string]ExportExpression     `json:"attributes,omitempty"`
	Blocks        map[string][]ExportBlock        `json:"blocks,omitempty"`
	DynamicBlocks map[string][]ExportDynamicBlock `json:"dynamic_blocks,omitempty"`
	Location      string                          `json:"location,omitempty"`
}

// ExportDynamicBlock is one `dynamic "<name>" { for_each = ...,
// iterator = ..., content { ... } }` instance. The block label (the
// "<name>" part) is the key in the parent's DynamicBlocks map and
// names the block type to generate at plan time.
//
// ForEach is the iteration source as an ExportExpression — the same
// {text, value?, ast?} shape used everywhere else, so converters
// can translate references to var.X / local.Y / resource.foo here.
//
// Iterator is the per-iteration variable name. When omitted in the
// source it defaults to the block label — emitted as empty here so
// consumers can choose whether to fold the default in. The content
// body's expressions reference iterator.value / iterator.key, so
// converters need this to map those refs to the target language's
// iteration variable.
//
// Content is the template body, reusing ExportBlock so dynamic-
// inside-dynamic (and static-inside-dynamic) work via the same
// recursion.
type ExportDynamicBlock struct {
	ForEach  *ExportExpression `json:"for_each,omitempty"`
	Iterator string            `json:"iterator,omitempty"`
	Content  ExportBlock       `json:"content"`
	Location string            `json:"location,omitempty"`
}

type ExportLocal struct {
	Name     string            `json:"name"`
	Value    *ExportExpression `json:"value,omitempty"`
	Location string            `json:"location,omitempty"`
}

type ExportModuleCall struct {
	Name      string                      `json:"name"`
	Source    string                      `json:"source,omitempty"`
	Version   string                      `json:"version,omitempty"`
	Arguments map[string]ExportExpression `json:"arguments,omitempty"`
	Count     *ExportExpression           `json:"count,omitempty"`
	ForEach   *ExportExpression           `json:"for_each,omitempty"`
	Location  string                      `json:"location,omitempty"`
}

type ExportTerraform struct {
	RequiredVersion   string                       `json:"required_version,omitempty"`
	RequiredProviders map[string]ExportProviderReq `json:"required_providers,omitempty"`
	Backend           *ExportBackend               `json:"backend,omitempty"`
	// Providers is the list of top-level `provider "X" { ... }` blocks
	// in source order — distinct from required_providers (the version
	// declarations). Each entry carries Type, Alias?, and the
	// per-attribute Config map. Resources select non-default instances
	// via the existing `provider` meta-arg → `provider = aws.east`
	// matches Type=aws + Alias=east.
	Providers []ExportProvider `json:"providers,omitempty"`
}

type ExportProviderReq struct {
	Source             string `json:"source,omitempty"`
	VersionConstraint  string `json:"version_constraint,omitempty"`
	ConfigurationAlias string `json:"configuration_alias,omitempty"`
}

// ExportProvider is one top-level `provider "X" { ... }` block.
// Alias is empty for the default instance. Config holds the per-
// attribute body (excluding `alias`, which is promoted to its own
// field) as the standard {text, value?, ast?} expression triple.
type ExportProvider struct {
	Type     string                      `json:"type"`
	Alias    string                      `json:"alias,omitempty"`
	Config   map[string]ExportExpression `json:"config,omitempty"`
	Location string                      `json:"location,omitempty"`
}

type ExportBackend struct {
	Type string `json:"type"`
}

type ExportTracked struct {
	Subject string `json:"subject"`
	// Note: ExpressionText (not the unified ExportExpression) because
	// pkg/analysis.TrackedAttribute keeps the underlying *Expr private
	// — we only have the canonical text. Promoting to the {text, value?, ast?}
	// shape needs a small pkg/analysis API addition; deferred.
	ExpressionText string `json:"expression_text,omitempty"`
	Marker         string `json:"marker,omitempty"`
	Location       string `json:"location,omitempty"`
}

// ExportCtyJSON wraps a cty.Value as its JSON representation plus the
// type marker. Both fields are RawMessage because cty/json already
// emits valid JSON (including the surrounding quotes for string-shaped
// type tags) — re-encoding via Go's json package would double-quote.
// Consumers can use the value directly or reconstruct the cty.Value
// via cty/json.Unmarshal(value, cty/json.UnmarshalType(type)).
type ExportCtyJSON struct {
	Type  json.RawMessage `json:"type"`
	Value json.RawMessage `json:"value"`
}

func ctyToExport(v cty.Value) *ExportCtyJSON {
	if v == cty.NilVal || !v.IsKnown() || v.IsNull() {
		return nil
	}
	raw, err := ctyjson.Marshal(v, v.Type())
	if err != nil {
		return nil
	}
	typeRaw, err := ctyjson.MarshalType(v.Type())
	if err != nil {
		// Fallback: emit the friendly name as a JSON string so the
		// shape stays consistent (always valid JSON in the type field).
		typeRaw, _ = json.Marshal(v.Type().FriendlyName())
	}
	return &ExportCtyJSON{Type: typeRaw, Value: raw}
}

// BuildExport walks the project tree and produces the Export envelope.
// tflensVersion is optional (callers can pass "" if unknown).
func BuildExport(p *loader.Project, tflensVersion string) Export {
	exp := Export{
		Experimental:  true,
		Warning:       "Output shape is experimental and subject to change. Do not depend on field stability across minor versions.",
		SchemaVersion: ExportSchemaVersion,
		TflensVersion: tflensVersion,
	}
	if p == nil || p.Root == nil {
		return exp
	}
	exp.Root = exportNode(p.Root, "", "")
	return exp
}

func exportNode(n *loader.ModuleNode, source, version string) ExportNode {
	out := ExportNode{
		Dir:     n.Dir,
		Source:  source,
		Version: version,
		Module:  exportModule(n.Module),
	}
	if len(n.Children) > 0 {
		out.Children = make(map[string]ExportNode, len(n.Children))
		for name, child := range n.Children {
			cs, cv := "", ""
			if n.Module != nil {
				cs = n.Module.ModuleSource(name)
				cv = n.Module.ModuleVersion(name)
			}
			out.Children[name] = exportNode(child, cs, cv)
		}
	}
	return out
}

func exportModule(m *analysis.Module) ExportModule {
	out := ExportModule{
		Variables:    []ExportVariable{},
		Outputs:      []ExportOutput{},
		Resources:    []ExportResource{},
		DataSources:  []ExportResource{},
		Locals:       []ExportLocal{},
		ModuleCalls:  []ExportModuleCall{},
		Tracked:      []ExportTracked{},
		Dependencies: map[string][]string{},
	}
	if m == nil {
		return out
	}
	ctx := m.EvalContext()
	for _, e := range m.Entities() {
		switch e.Kind {
		case analysis.KindVariable:
			out.Variables = append(out.Variables, exportVariable(e, ctx))
		case analysis.KindOutput:
			out.Outputs = append(out.Outputs, exportOutput(e, ctx))
		case analysis.KindResource:
			out.Resources = append(out.Resources, exportResource(e, ctx))
		case analysis.KindData:
			out.DataSources = append(out.DataSources, exportResource(e, ctx))
		case analysis.KindLocal:
			out.Locals = append(out.Locals, exportLocal(e, ctx))
		case analysis.KindModule:
			out.ModuleCalls = append(out.ModuleCalls, exportModuleCall(e, m, ctx))
		}
		// Dependency adjacency — emitted for every entity, not just one
		// kind, so converters get the full graph.
		if deps := m.Dependencies(e.ID()); len(deps) > 0 {
			sorted := append([]string(nil), deps...)
			sort.Strings(sorted)
			out.Dependencies[e.ID()] = sorted
		}
	}
	out.Terraform = exportTerraform(m, ctx)
	out.Tracked = exportTrackedAttributes(m, ctx)

	// Stable per-section ordering — converters depend on deterministic output.
	sortByName := func(less func(i, j int) bool) func(int, int) bool { return less }
	sort.Slice(out.Variables, sortByName(func(i, j int) bool { return out.Variables[i].Name < out.Variables[j].Name }))
	sort.Slice(out.Outputs, sortByName(func(i, j int) bool { return out.Outputs[i].Name < out.Outputs[j].Name }))
	sort.Slice(out.Locals, sortByName(func(i, j int) bool { return out.Locals[i].Name < out.Locals[j].Name }))
	sort.Slice(out.ModuleCalls, sortByName(func(i, j int) bool { return out.ModuleCalls[i].Name < out.ModuleCalls[j].Name }))
	sort.Slice(out.Resources, func(i, j int) bool {
		if out.Resources[i].Type != out.Resources[j].Type {
			return out.Resources[i].Type < out.Resources[j].Type
		}
		return out.Resources[i].Name < out.Resources[j].Name
	})
	sort.Slice(out.DataSources, func(i, j int) bool {
		if out.DataSources[i].Type != out.DataSources[j].Type {
			return out.DataSources[i].Type < out.DataSources[j].Type
		}
		return out.DataSources[i].Name < out.DataSources[j].Name
	})
	sort.Slice(out.Tracked, func(i, j int) bool { return out.Tracked[i].Subject < out.Tracked[j].Subject })
	return out
}

// exprToExport is the universal builder for ExportExpression — every
// place that emits an HCL expression goes through this so the {text,
// value?, ast?} contract is identical across attributes, count,
// for_each, depends_on, lifecycle, module-call arguments, locals,
// outputs, variable defaults, etc. Returns nil for a nil/empty *Expr
// so callers can plug it directly into pointer-typed export fields
// (the zero result then disappears thanks to omitempty).
func exprToExport(e *analysis.Expr, ctx *hcl.EvalContext) *ExportExpression {
	if e == nil || e.E == nil {
		return nil
	}
	return &ExportExpression{
		Text:  e.Text(),
		Value: evalToExport(e, ctx),
		AST:   astFor(e),
	}
}

func exportVariable(e analysis.Entity, ctx *hcl.EvalContext) ExportVariable {
	v := ExportVariable{
		Name:        e.Name,
		HasDefault:  e.HasDefault,
		Sensitive:   e.Sensitive,
		Ephemeral:   e.Ephemeral,
		Nullable:    !e.NonNullable,
		Validations: exportConditionBlocks(e.Validations, ctx),
		Location:    e.Location(),
	}
	if e.DeclaredType != nil {
		v.Type = e.DeclaredType.String()
	}
	v.Default = exprToExport(e.DefaultExpr, ctx)
	return v
}

func exportOutput(e analysis.Entity, ctx *hcl.EvalContext) ExportOutput {
	return ExportOutput{
		Name:           e.Name,
		Value:          exprToExport(e.ValueExpr, ctx),
		Sensitive:      e.Sensitive,
		Ephemeral:      e.Ephemeral,
		DependsOn:      exprToExport(e.DependsOnExpr, ctx),
		Preconditions:  exportConditionBlocks(e.Preconditions, ctx),
		Postconditions: exportConditionBlocks(e.Postconditions, ctx),
		Location:       e.Location(),
	}
}

// exportConditionBlocks converts the analysis-side ConditionBlock
// slice into the wire-format ExportConditionBlock list. Both inner
// expressions go through exprToExport so consumers get the same
// {text, value?, ast?} triple they get for every other expression in
// the export. Nil/empty input → nil output (so the omitempty tag
// drops the whole field).
func exportConditionBlocks(blocks []analysis.ConditionBlock, ctx *hcl.EvalContext) []ExportConditionBlock {
	if len(blocks) == 0 {
		return nil
	}
	out := make([]ExportConditionBlock, len(blocks))
	for i, b := range blocks {
		entry := ExportConditionBlock{
			Condition:    exprToExport(b.Condition, ctx),
			ErrorMessage: exprToExport(b.ErrorMessage, ctx),
		}
		if b.Pos.File != "" {
			entry.Location = filepath.Base(b.Pos.File) + ":" + strconv.Itoa(b.Pos.Line)
		}
		out[i] = entry
	}
	return out
}

func exportResource(e analysis.Entity, ctx *hcl.EvalContext) ExportResource {
	r := ExportResource{
		Type:                e.Type,
		Name:                e.Name,
		Provider:            exprToExport(e.ProviderExpr, ctx),
		Count:               exprToExport(e.CountExpr, ctx),
		ForEach:             exprToExport(e.ForEachExpr, ctx),
		DependsOn:           exprToExport(e.DependsOnExpr, ctx),
		PreventDestroy:      e.PreventDestroy,
		CreateBeforeDestroy: e.CreateBeforeDestroy,
		IgnoreChanges:       exprToExport(e.IgnoreChangesExpr, ctx),
		ReplaceTriggeredBy:  exprToExport(e.ReplaceTriggeredByExpr, ctx),
		Preconditions:       exportConditionBlocks(e.Preconditions, ctx),
		Postconditions:      exportConditionBlocks(e.Postconditions, ctx),
		Location:            e.Location(),
	}
	if len(e.BodyAttrs) > 0 {
		r.Attributes = make(map[string]ExportExpression, len(e.BodyAttrs))
		for name, expr := range e.BodyAttrs {
			r.Attributes[name] = *exprToExport(expr, ctx)
		}
	}
	if len(e.BodyBlocks) > 0 {
		r.Blocks = make(map[string][]ExportBlock, len(e.BodyBlocks))
		for name, instances := range e.BodyBlocks {
			out := make([]ExportBlock, 0, len(instances))
			for _, b := range instances {
				out = append(out, exportBlock(b, ctx))
			}
			r.Blocks[name] = out
		}
	}
	if len(e.BodyDynamicBlocks) > 0 {
		r.DynamicBlocks = make(map[string][]ExportDynamicBlock, len(e.BodyDynamicBlocks))
		for name, instances := range e.BodyDynamicBlocks {
			out := make([]ExportDynamicBlock, 0, len(instances))
			for _, d := range instances {
				out = append(out, exportDynamicBlock(d, ctx))
			}
			r.DynamicBlocks[name] = out
		}
	}
	return r
}

// exportBlock recursively converts a *analysis.BodyBlock into an
// ExportBlock. Mirrors the parent-resource shape (attributes +
// nested blocks) so consumers walk one type at every depth.
func exportBlock(b *analysis.BodyBlock, ctx *hcl.EvalContext) ExportBlock {
	out := ExportBlock{}
	if b == nil {
		return out
	}
	if b.Pos.File != "" {
		out.Location = filepath.Base(b.Pos.File) + ":" + strconv.Itoa(b.Pos.Line)
	}
	if len(b.Attrs) > 0 {
		out.Attributes = make(map[string]ExportExpression, len(b.Attrs))
		for name, expr := range b.Attrs {
			out.Attributes[name] = *exprToExport(expr, ctx)
		}
	}
	if len(b.Blocks) > 0 {
		out.Blocks = make(map[string][]ExportBlock, len(b.Blocks))
		for name, instances := range b.Blocks {
			children := make([]ExportBlock, 0, len(instances))
			for _, c := range instances {
				children = append(children, exportBlock(c, ctx))
			}
			out.Blocks[name] = children
		}
	}
	if len(b.DynamicBlocks) > 0 {
		out.DynamicBlocks = make(map[string][]ExportDynamicBlock, len(b.DynamicBlocks))
		for name, instances := range b.DynamicBlocks {
			children := make([]ExportDynamicBlock, 0, len(instances))
			for _, d := range instances {
				children = append(children, exportDynamicBlock(d, ctx))
			}
			out.DynamicBlocks[name] = children
		}
	}
	return out
}

// exportDynamicBlock converts a *analysis.BodyDynamicBlock into the
// wire-format ExportDynamicBlock. Content recurses through exportBlock
// so dynamic-inside-static and dynamic-inside-dynamic both work.
//
// The for_each expression goes through exprToExport like every other
// HCL expression (text + value + AST), even when it would resolve
// statically — converters need the structural shape to translate
// to the target system's iteration primitive (kro for-each, crossplane
// composition, …).
func exportDynamicBlock(d *analysis.BodyDynamicBlock, ctx *hcl.EvalContext) ExportDynamicBlock {
	out := ExportDynamicBlock{
		ForEach:  exprToExport(d.ForEach, ctx),
		Iterator: d.Iterator,
	}
	if d.Pos.File != "" {
		out.Location = filepath.Base(d.Pos.File) + ":" + strconv.Itoa(d.Pos.Line)
	}
	if d.Content != nil {
		out.Content = exportBlock(d.Content, ctx)
	}
	return out
}

func exportLocal(e analysis.Entity, ctx *hcl.EvalContext) ExportLocal {
	return ExportLocal{
		Name:     e.Name,
		Value:    exprToExport(e.LocalExpr, ctx),
		Location: e.Location(),
	}
}

func exportModuleCall(e analysis.Entity, m *analysis.Module, ctx *hcl.EvalContext) ExportModuleCall {
	c := ExportModuleCall{
		Name:     e.Name,
		Source:   m.ModuleSource(e.Name),
		Version:  m.ModuleVersion(e.Name),
		Count:    exprToExport(e.CountExpr, ctx),
		ForEach:  exprToExport(e.ForEachExpr, ctx),
		Location: e.Location(),
	}
	if len(e.ModuleArgs) > 0 {
		c.Arguments = make(map[string]ExportExpression, len(e.ModuleArgs))
		for k, v := range e.ModuleArgs {
			c.Arguments[k] = *exprToExport(v, ctx)
		}
	}
	return c
}

func exportTerraform(m *analysis.Module, ctx *hcl.EvalContext) ExportTerraform {
	t := ExportTerraform{
		RequiredVersion: m.RequiredVersion(),
	}
	if reqs := m.RequiredProviders(); len(reqs) > 0 {
		t.RequiredProviders = make(map[string]ExportProviderReq, len(reqs))
		for name, r := range reqs {
			t.RequiredProviders[name] = ExportProviderReq{
				Source:            r.Source,
				VersionConstraint: r.Version,
			}
		}
	}
	if b := m.Backend(); b != nil {
		t.Backend = &ExportBackend{Type: b.Type}
	}
	if provs := m.Providers(); len(provs) > 0 {
		t.Providers = make([]ExportProvider, 0, len(provs))
		for _, p := range provs {
			ep := ExportProvider{Type: p.Type, Alias: p.Alias}
			if p.Pos.File != "" {
				ep.Location = filepath.Base(p.Pos.File) + ":" + strconv.Itoa(p.Pos.Line)
			}
			if len(p.Config) > 0 {
				ep.Config = make(map[string]ExportExpression, len(p.Config))
				for name, expr := range p.Config {
					ep.Config[name] = *exprToExport(expr, ctx)
				}
			}
			t.Providers = append(t.Providers, ep)
		}
	}
	return t
}

func exportTrackedAttributes(m *analysis.Module, _ *hcl.EvalContext) []ExportTracked {
	// Note: TrackedAttribute exposes ExprText (canonical text) but not
	// the raw expression — that's an internal of pkg/analysis. The
	// export emits the text only; converters can re-parse if they want
	// re-evaluation, or use the per-local evaluated_value fields when
	// the tracked attribute references a local.
	tracked := m.TrackedAttributes()
	out := make([]ExportTracked, 0, len(tracked))
	for _, ta := range tracked {
		t := ExportTracked{
			Subject:        ta.Key(),
			ExpressionText: ta.ExprText,
			Marker:         ta.Description,
		}
		if ta.Pos.File != "" {
			// Match the basename convention used by Entity.Location()
			// (file:line, not abspath:line) so consumers see consistent
			// short locations across all entity kinds.
			t.Location = filepath.Base(ta.Pos.File) + ":" + strconv.Itoa(ta.Pos.Line)
		}
		out = append(out, t)
	}
	return out
}

// evalToExport tries to evaluate the expression against the module's
// EvalContext. Returns nil when the expression can't be statically
// resolved (e.g. references a data source, computed attribute, or
// non-curated function) — that's the conservative-fallback principle:
// converters get the text and can choose what to do with unevaluable
// expressions.
func evalToExport(e *analysis.Expr, ctx *hcl.EvalContext) *ExportCtyJSON {
	if e == nil || e.E == nil || ctx == nil {
		return nil
	}
	v, diags := e.E.Value(ctx)
	if diags.HasErrors() {
		return nil
	}
	return ctyToExport(v)
}

// WriteExport marshals exp as pretty-printed JSON to w.
func WriteExport(exp Export, w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(exp)
}
