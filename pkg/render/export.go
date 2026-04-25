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

const ExportSchemaVersion = "0.1.0-prototype"

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
	Name         string         `json:"name"`
	Type         string         `json:"type,omitempty"`
	HasDefault   bool           `json:"has_default"`
	DefaultText  string         `json:"default_text,omitempty"`
	DefaultValue *ExportCtyJSON `json:"default_value,omitempty"`
	Sensitive    bool           `json:"sensitive,omitempty"`
	Ephemeral    bool           `json:"ephemeral,omitempty"`
	Nullable     bool           `json:"nullable"`
	Validations  int            `json:"validation_count,omitempty"`
	Location     string         `json:"location,omitempty"`
}

type ExportOutput struct {
	Name           string         `json:"name"`
	ValueText      string         `json:"value_text,omitempty"`
	EvaluatedValue *ExportCtyJSON `json:"evaluated_value,omitempty"`
	Sensitive      bool           `json:"sensitive,omitempty"`
	Ephemeral      bool           `json:"ephemeral,omitempty"`
	DependsOn      string         `json:"depends_on,omitempty"`
	Location       string         `json:"location,omitempty"`
}

type ExportResource struct {
	Type                   string                     `json:"type"`
	Name                   string                     `json:"name"`
	Provider               string                     `json:"provider,omitempty"`
	CountText              string                     `json:"count_text,omitempty"`
	ForEachText            string                     `json:"for_each_text,omitempty"`
	DependsOnText          string                     `json:"depends_on_text,omitempty"`
	PreventDestroy         bool                       `json:"prevent_destroy,omitempty"`
	CreateBeforeDestroy    bool                       `json:"create_before_destroy,omitempty"`
	IgnoreChangesText      string                     `json:"ignore_changes_text,omitempty"`
	ReplaceTriggeredByText string                     `json:"replace_triggered_by_text,omitempty"`
	Attributes             map[string]ExportAttribute `json:"attributes,omitempty"`
	Location               string                     `json:"location,omitempty"`
	// Note: nested blocks inside resource bodies (lifecycle is the
	// only one currently captured into dedicated fields; ebs_block_device,
	// ingress, dynamic, ...) are still not surfaced. Deferred.
}

// ExportAttribute pairs the canonical source text of a resource's
// attribute with its statically-evaluated cty value when one can be
// resolved. Same shape as locals/variables — converters that need a
// clean structured value get one for literal-heavy attributes (`tags = { Name = "web" }`),
// while expressions referencing data sources or computed attributes
// fall back to the text-only form.
type ExportAttribute struct {
	Text  string         `json:"text"`
	Value *ExportCtyJSON `json:"value,omitempty"`
}

type ExportLocal struct {
	Name           string         `json:"name"`
	ValueText      string         `json:"value_text,omitempty"`
	EvaluatedValue *ExportCtyJSON `json:"evaluated_value,omitempty"`
	Location       string         `json:"location,omitempty"`
}

type ExportModuleCall struct {
	Name        string            `json:"name"`
	Source      string            `json:"source,omitempty"`
	Version     string            `json:"version,omitempty"`
	Arguments   map[string]string `json:"arguments,omitempty"`
	CountText   string            `json:"count_text,omitempty"`
	ForEachText string            `json:"for_each_text,omitempty"`
	Location    string            `json:"location,omitempty"`
}

type ExportTerraform struct {
	RequiredVersion   string                       `json:"required_version,omitempty"`
	RequiredProviders map[string]ExportProviderReq `json:"required_providers,omitempty"`
	Backend           *ExportBackend               `json:"backend,omitempty"`
}

type ExportProviderReq struct {
	Source             string `json:"source,omitempty"`
	VersionConstraint  string `json:"version_constraint,omitempty"`
	ConfigurationAlias string `json:"configuration_alias,omitempty"`
}

type ExportBackend struct {
	Type string `json:"type"`
}

type ExportTracked struct {
	Subject        string         `json:"subject"`
	ExpressionText string         `json:"expression_text,omitempty"`
	EvaluatedValue *ExportCtyJSON `json:"evaluated_value,omitempty"`
	Marker         string         `json:"marker,omitempty"`
	Location       string         `json:"location,omitempty"`
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
			out.ModuleCalls = append(out.ModuleCalls, exportModuleCall(e, m))
		}
		// Dependency adjacency — emitted for every entity, not just one
		// kind, so converters get the full graph.
		if deps := m.Dependencies(e.ID()); len(deps) > 0 {
			sorted := append([]string(nil), deps...)
			sort.Strings(sorted)
			out.Dependencies[e.ID()] = sorted
		}
	}
	out.Terraform = exportTerraform(m)
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

func exportVariable(e analysis.Entity, ctx *hcl.EvalContext) ExportVariable {
	v := ExportVariable{
		Name:        e.Name,
		HasDefault:  e.HasDefault,
		Sensitive:   e.Sensitive,
		Ephemeral:   e.Ephemeral,
		Nullable:    !e.NonNullable,
		Validations: e.Validations,
		Location:    e.Location(),
	}
	if e.DeclaredType != nil {
		v.Type = e.DeclaredType.String()
	}
	if e.DefaultExpr != nil {
		v.DefaultText = e.DefaultExpr.Text()
		v.DefaultValue = evalToExport(e.DefaultExpr, ctx)
	}
	return v
}

func exportOutput(e analysis.Entity, ctx *hcl.EvalContext) ExportOutput {
	o := ExportOutput{
		Name:      e.Name,
		Sensitive: e.Sensitive,
		Ephemeral: e.Ephemeral,
		Location:  e.Location(),
	}
	if e.ValueExpr != nil {
		o.ValueText = e.ValueExpr.Text()
		o.EvaluatedValue = evalToExport(e.ValueExpr, ctx)
	}
	if e.DependsOnExpr != nil {
		o.DependsOn = e.DependsOnExpr.Text()
	}
	return o
}

func exportResource(e analysis.Entity, ctx *hcl.EvalContext) ExportResource {
	r := ExportResource{
		Type:                e.Type,
		Name:                e.Name,
		PreventDestroy:      e.PreventDestroy,
		CreateBeforeDestroy: e.CreateBeforeDestroy,
		Location:            e.Location(),
	}
	if e.ProviderExpr != nil {
		r.Provider = e.ProviderExpr.Text()
	}
	if e.CountExpr != nil {
		r.CountText = e.CountExpr.Text()
	}
	if e.ForEachExpr != nil {
		r.ForEachText = e.ForEachExpr.Text()
	}
	if e.DependsOnExpr != nil {
		r.DependsOnText = e.DependsOnExpr.Text()
	}
	if e.IgnoreChangesExpr != nil {
		r.IgnoreChangesText = e.IgnoreChangesExpr.Text()
	}
	if e.ReplaceTriggeredByExpr != nil {
		r.ReplaceTriggeredByText = e.ReplaceTriggeredByExpr.Text()
	}
	if len(e.BodyAttrs) > 0 {
		r.Attributes = make(map[string]ExportAttribute, len(e.BodyAttrs))
		for name, expr := range e.BodyAttrs {
			r.Attributes[name] = ExportAttribute{
				Text:  expr.Text(),
				Value: evalToExport(expr, ctx),
			}
		}
	}
	return r
}

func exportLocal(e analysis.Entity, ctx *hcl.EvalContext) ExportLocal {
	l := ExportLocal{
		Name:     e.Name,
		Location: e.Location(),
	}
	if e.LocalExpr != nil {
		l.ValueText = e.LocalExpr.Text()
		l.EvaluatedValue = evalToExport(e.LocalExpr, ctx)
	}
	return l
}

func exportModuleCall(e analysis.Entity, m *analysis.Module) ExportModuleCall {
	c := ExportModuleCall{
		Name:     e.Name,
		Source:   m.ModuleSource(e.Name),
		Version:  m.ModuleVersion(e.Name),
		Location: e.Location(),
	}
	if e.CountExpr != nil {
		c.CountText = e.CountExpr.Text()
	}
	if e.ForEachExpr != nil {
		c.ForEachText = e.ForEachExpr.Text()
	}
	if len(e.ModuleArgs) > 0 {
		c.Arguments = make(map[string]string, len(e.ModuleArgs))
		for k, v := range e.ModuleArgs {
			c.Arguments[k] = v.Text()
		}
	}
	return c
}

func exportTerraform(m *analysis.Module) ExportTerraform {
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
