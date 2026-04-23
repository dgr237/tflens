package analysis_test

import (
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
)

// analysisCase describes one TestAnalysisCases case. Fixtures live under
// pkg/analysis/testdata/analysis/<Name>/main.tf. The case can assert one
// or more dependency edges via HasDeps; everything else (entity counts,
// position checks, dependents, DOT output) goes through Custom because
// each shape is too varied for a uniform field.
type analysisCase struct {
	Name string

	// Filename used when parsing the fixture; relevant for
	// position-based assertions.
	Filename string

	// HasDeps is a list of {from, to} pairs that must each be present in
	// the analysed module's dependency graph.
	HasDeps [][2]string

	Custom func(t *testing.T, m *analysis.Module)
}

func TestAnalysisCases(t *testing.T) {
	for _, tc := range analysisCases {
		t.Run(tc.Name, func(t *testing.T) {
			src := loadAnalysisFixture(t, "analysis", tc.Name)
			fname := tc.Filename
			if fname == "" {
				fname = "main.tf"
			}
			m := analyseFixtureNamed(t, fname, src)
			for _, dep := range tc.HasDeps {
				if !m.HasDep(dep[0], dep[1]) {
					t.Errorf("expected dep %s → %s; got deps for %s = %v",
						dep[0], dep[1], dep[0], m.Dependencies(dep[0]))
				}
			}
			if tc.Custom != nil {
				tc.Custom(t, m)
			}
		})
	}
}

var analysisCases = []analysisCase{
	{
		Name: "entity_counts",
		Custom: func(t *testing.T, m *analysis.Module) {
			check := func(kind analysis.EntityKind, want int) {
				t.Helper()
				if got := len(m.Filter(kind)); got != want {
					t.Errorf("%s count: got %d, want %d", kind, got, want)
				}
			}
			check(analysis.KindVariable, 2)
			check(analysis.KindLocal, 2)
			check(analysis.KindData, 1)
			check(analysis.KindResource, 2)
			check(analysis.KindOutput, 1)
		},
	},
	{
		Name: "entity_ids",
		Custom: func(t *testing.T, m *analysis.Module) {
			want := map[string]bool{
				"variable.env":              true,
				"data.aws_ami.ubuntu":       true,
				"resource.aws_instance.web": true,
				"output.id":                 true,
			}
			for _, e := range m.Entities() {
				delete(want, e.ID())
			}
			for id := range want {
				t.Errorf("missing entity: %s", id)
			}
		},
	},

	// ---- dependency edges ----
	{
		Name: "var_dependency",
		HasDeps: [][2]string{{"local.prefix", "variable.env"}},
	},
	{
		Name: "local_to_local_dependency",
		HasDeps: [][2]string{{"local.count", "local.is_prod"}},
	},
	{
		Name: "resource_to_data_dependency",
		HasDeps: [][2]string{{"resource.aws_instance.web", "data.aws_ami.ubuntu"}},
	},
	{
		Name: "resource_to_resource_dependency",
		HasDeps: [][2]string{{"resource.aws_subnet.pub", "resource.aws_vpc.main"}},
	},
	{
		Name: "output_dependency",
		HasDeps: [][2]string{{"output.vpc_id", "resource.aws_vpc.main"}},
	},
	{
		Name: "dep_in_nested_block",
		HasDeps: [][2]string{{"resource.aws_security_group.web", "resource.aws_vpc.main"}},
	},
	{
		Name: "dep_in_template_string",
		HasDeps: [][2]string{{"local.name", "variable.env"}},
	},
	{
		Name: "dep_in_for_expr",
		HasDeps: [][2]string{{"local.ids", "resource.aws_instance.web"}},
	},
	{
		Name: "no_deps_for_unknown_ref",
		Custom: func(t *testing.T, m *analysis.Module) {
			for _, d := range m.Dependencies("resource.aws_subnet.pub") {
				if d == "count.index" {
					t.Error("count.index should not appear as a dependency")
				}
			}
		},
	},
	{
		Name: "dependents",
		Custom: func(t *testing.T, m *analysis.Module) {
			want := map[string]bool{
				"resource.aws_subnet.pub":         true,
				"resource.aws_security_group.web": true,
			}
			for _, d := range m.Dependents("resource.aws_vpc.main") {
				delete(want, d)
			}
			if len(want) > 0 {
				t.Errorf("missing dependents: %v", want)
			}
		},
	},

	// ---- DOT output ----
	{
		Name: "to_dot_contains_nodes",
		Custom: func(t *testing.T, m *analysis.Module) {
			dot := m.ToDOT()
			for _, want := range []string{"variable.env", "resource.aws_vpc.main", "->"} {
				if !strings.Contains(dot, want) {
					t.Errorf("DOT output missing %q:\n%s", want, dot)
				}
			}
		},
	},

	// ---- source locations ----
	{
		Name: "entity_position_file", Filename: "infra.tf",
		Custom: func(t *testing.T, m *analysis.Module) {
			for _, e := range m.Entities() {
				if e.Pos.File != "infra.tf" {
					t.Errorf("entity %s: Pos.File = %q, want %q", e.ID(), e.Pos.File, "infra.tf")
				}
				if e.Location() == "" {
					t.Errorf("entity %s: Location() returned empty string", e.ID())
				}
			}
		},
	},
	{
		Name: "entity_position_line",
		Custom: func(t *testing.T, m *analysis.Module) {
			byID := map[string]int{}
			for _, e := range m.Entities() {
				byID[e.ID()] = e.Pos.Line
			}
			cases := []struct {
				id   string
				line int
			}{
				{"variable.a", 2},
				{"variable.b", 3},
				{"local.x", 5},
				{"local.y", 6},
				{"resource.aws_vpc.main", 8},
				{"output.id", 9},
			}
			for _, c := range cases {
				if got, ok := byID[c.id]; !ok {
					t.Errorf("entity %s not found", c.id)
				} else if got != c.line {
					t.Errorf("entity %s: line %d, want %d", c.id, got, c.line)
				}
			}
		},
	},
	{
		Name: "local_position_is_attribute_not_block",
		Custom: func(t *testing.T, m *analysis.Module) {
			lineA, lineB := 0, 0
			for _, e := range m.Entities() {
				switch e.ID() {
				case "local.a":
					lineA = e.Pos.Line
				case "local.b":
					lineB = e.Pos.Line
				}
			}
			if lineA != 2 {
				t.Errorf("local.a: line %d, want 2", lineA)
			}
			if lineB != 3 {
				t.Errorf("local.b: line %d, want 3", lineB)
			}
		},
	},
	{
		Name: "location_method", Filename: "/some/path/variables.tf",
		Custom: func(t *testing.T, m *analysis.Module) {
			vs := m.Filter(analysis.KindVariable)
			if len(vs) != 1 {
				t.Fatalf("expected 1 variable, got %d", len(vs))
			}
			if loc := vs[0].Location(); loc != "variables.tf:1" {
				t.Errorf("Location() = %q, want %q", loc, "variables.tf:1")
			}
		},
	},
}
