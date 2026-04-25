package stdlib_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"

	"github.com/dgr237/tflens/pkg/analysis/stdlib"
)

// stdlibCase pairs a fixture name with the cty.Value the local
// `out = <expr>` should evaluate to. Per-function fixtures live
// under pkg/analysis/stdlib/testdata/<Name>/main.tf — exactly one
// `locals { out = <expr> }` block apiece.
//
// The driver loads the fixture, evaluates the local's expression
// against an EvalContext that has only stdlib.Functions() wired in
// (no module variables or other locals), and compares the result
// against Want via cty.Value.RawEquals — exact-equality including
// type, so a list-vs-set or string-vs-number mismatch fails.
type stdlibCase struct {
	Name string
	Want cty.Value
}

func TestStdlibFunctionCases(t *testing.T) {
	for _, tc := range stdlibCases {
		t.Run(tc.Name, func(t *testing.T) {
			got := evalFixtureLocal(t, tc.Name)
			if !got.RawEquals(tc.Want) {
				t.Errorf("%s: got %#v, want %#v", tc.Name, got, tc.Want)
			}
		})
	}
}

var stdlibCases = []stdlibCase{
	// Type conversion
	{
		Name: "toset",
		Want: cty.SetVal([]cty.Value{cty.StringVal("us-east-1"), cty.StringVal("us-west-2")}),
	},
	{
		Name: "tolist",
		// tolist of a set returns the set's elements in cty's set
		// iteration order, which sorts strings lexicographically.
		Want: cty.ListVal([]cty.Value{cty.StringVal("a"), cty.StringVal("b"), cty.StringVal("c")}),
	},
	{
		Name: "tomap",
		Want: cty.MapVal(map[string]cty.Value{
			"k1": cty.StringVal("v1"), "k2": cty.StringVal("v2"),
		}),
	},
	{
		Name: "tostring",
		Want: cty.StringVal("42"),
	},
	{
		Name: "tonumber",
		Want: cty.NumberIntVal(42),
	},
	{
		Name: "tobool",
		Want: cty.True,
	},

	// Collections
	{
		Name: "length",
		Want: cty.NumberIntVal(4),
	},
	{
		// concat of tuple literals returns a tuple, not a list — the
		// inputs are typed as tuples by the HCL literal-typer because
		// they could (in principle) hold heterogeneous types.
		Name: "concat",
		Want: cty.TupleVal([]cty.Value{
			cty.StringVal("a"), cty.StringVal("b"), cty.StringVal("c"),
			cty.StringVal("d"), cty.StringVal("e"),
		}),
	},
	{
		Name: "merge",
		// merge of two single-key object literals yields an object
		// (not a map) whose attribute set is the union of the inputs.
		Want: cty.ObjectVal(map[string]cty.Value{
			"a": cty.NumberIntVal(1), "b": cty.NumberIntVal(2),
		}),
	},
	{
		// Later argument wins on overlap — Terraform-spec behaviour.
		Name: "merge_overlapping_keys",
		Want: cty.ObjectVal(map[string]cty.Value{"k": cty.StringVal("last")}),
	},
	{
		// keys of an OBJECT (HCL { "z" = 1, ... } literal) returns a
		// tuple sorted lexicographically by key. keys of a MAP would
		// return a list — see the `keys` of `tomap(...)` case in a
		// future batch if we need to pin both shapes.
		Name: "keys",
		Want: cty.TupleVal([]cty.Value{
			cty.StringVal("a"), cty.StringVal("m"), cty.StringVal("z"),
		}),
	},
	{
		// values follows the same sort-by-key ordering as keys, so
		// values[i] corresponds to keys[i]. Returns a tuple here for
		// the same object-vs-map reason as keys.
		Name: "values",
		Want: cty.TupleVal([]cty.Value{
			cty.StringVal("A"), cty.StringVal("M"), cty.StringVal("Z"),
		}),
	},
	{
		Name: "lookup_present",
		Want: cty.StringVal("v"),
	},
	{
		Name: "lookup_default",
		Want: cty.StringVal("fallback"),
	},
	{
		Name: "contains_true",
		Want: cty.True,
	},
	{
		Name: "contains_false",
		Want: cty.False,
	},
	{
		Name: "element",
		Want: cty.StringVal("b"),
	},
	{
		// flatten of nested tuple literals returns a tuple — the
		// outer-vs-inner type promotion to list only happens when
		// every nested element shares one type AND the outer was a
		// list to begin with.
		Name: "flatten",
		Want: cty.TupleVal([]cty.Value{
			cty.StringVal("a"), cty.StringVal("b"), cty.StringVal("c"),
			cty.StringVal("d"), cty.StringVal("e"),
		}),
	},
	{
		Name: "distinct",
		Want: cty.ListVal([]cty.Value{
			cty.StringVal("a"), cty.StringVal("b"), cty.StringVal("c"),
		}),
	},
}

// TestFunctionsReturnsExpectedSet pins the public surface — the
// curated function set declared in this batch. Adding a new function
// requires updating both the map AND this test.
func TestFunctionsReturnsExpectedSet(t *testing.T) {
	want := []string{
		"toset", "tolist", "tomap", "tostring", "tonumber", "tobool",
		"length", "concat", "merge", "keys", "values",
		"lookup", "contains", "element", "flatten", "distinct",
	}
	got := stdlib.Functions()
	if len(got) != len(want) {
		t.Errorf("Functions() len = %d, want %d", len(got), len(want))
	}
	for _, name := range want {
		if _, ok := got[name]; !ok {
			t.Errorf("missing function %q in Functions()", name)
		}
	}
}

// TestFunctionsReturnsFreshMap confirms the per-call freshness
// contract — mutating the returned map must not affect a subsequent
// call. This lets tests swap implementations safely.
func TestFunctionsReturnsFreshMap(t *testing.T) {
	first := stdlib.Functions()
	delete(first, "length")
	second := stdlib.Functions()
	if _, ok := second["length"]; !ok {
		t.Error("Functions() should return a fresh map; mutation leaked across calls")
	}
}

// ---- helpers ----

// evalFixtureLocal loads pkg/analysis/stdlib/testdata/<name>/main.tf,
// finds the `locals { out = <expr> }` block, and evaluates `out`
// against an EvalContext containing only stdlib.Functions() — no
// variables, no other locals. Returns the evaluated cty.Value.
func evalFixtureLocal(t *testing.T, name string) cty.Value {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	src, err := os.ReadFile(filepath.Join(filepath.Dir(file), "testdata", name, "main.tf"))
	if err != nil {
		t.Fatalf("read fixture %s: %v", name, err)
	}
	p := hclparse.NewParser()
	f, diags := p.ParseHCL(src, "main.tf")
	if diags.HasErrors() {
		t.Fatalf("parse %s: %v", name, diags)
	}
	body := f.Body.(*hclsyntax.Body)
	for _, block := range body.Blocks {
		if block.Type != "locals" {
			continue
		}
		attr, ok := block.Body.Attributes["out"]
		if !ok {
			continue
		}
		ctx := &hcl.EvalContext{Functions: stdlib.Functions()}
		v, diags := attr.Expr.Value(ctx)
		if diags.HasErrors() {
			t.Fatalf("evaluating %s.out: %v", name, diags)
		}
		return v
	}
	t.Fatalf("fixture %s missing `locals { out = ... }` block", name)
	return cty.NilVal
}
