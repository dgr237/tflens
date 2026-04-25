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

	// String functions
	{Name: "upper", Want: cty.StringVal("HELLO")},
	{Name: "lower", Want: cty.StringVal("hello")},
	{Name: "join", Want: cty.StringVal("a-b-c")},
	{
		// split returns list(string), not tuple — explicit element type.
		Name: "split",
		Want: cty.ListVal([]cty.Value{
			cty.StringVal("a"), cty.StringVal("b"), cty.StringVal("c"),
		}),
	},
	{Name: "format", Want: cty.StringVal("hello world, 42")},
	{
		// Literal mode — substr is plain text, no `/.../` delimiters.
		Name: "replace_literal",
		Want: cty.StringVal("baz bar baz"),
	},
	{
		// Regex mode — `/[0-9]+/` triggers the Terraform-side
		// dispatcher in replace.go (cty's own ReplaceFunc would treat
		// the slashes as literal).
		Name: "replace_regex",
		Want: cty.StringVal("abcNdef"),
	},
	{Name: "trim", Want: cty.StringVal("hello")},
	{Name: "trimspace", Want: cty.StringVal("hello")},
	{Name: "trimprefix", Want: cty.StringVal("world")},
	{Name: "trimsuffix", Want: cty.StringVal("hello")},

	// Numeric functions
	{Name: "abs", Want: cty.NumberIntVal(5)},
	{Name: "min", Want: cty.NumberIntVal(1)},
	{Name: "max", Want: cty.NumberIntVal(3)},
	{Name: "floor", Want: cty.NumberIntVal(3)},
	{Name: "ceil", Want: cty.NumberIntVal(4)},
	{Name: "pow", Want: cty.NumberIntVal(1024)},

	// Additional batch-2 string helpers
	{Name: "title", Want: cty.StringVal("Hello World")},
	{Name: "substr", Want: cty.StringVal("cde")},
	{Name: "chomp", Want: cty.StringVal("hello")},
	{Name: "indent", Want: cty.StringVal("line1\n  line2\n  line3")},
	{
		Name: "formatlist",
		Want: cty.ListVal([]cty.Value{
			cty.StringVal("hi-a"), cty.StringVal("hi-b"), cty.StringVal("hi-c"),
		}),
	},

	// Additional batch-2 collection helpers
	{
		// SortFunc returns list(string).
		Name: "sort",
		Want: cty.ListVal([]cty.Value{
			cty.StringVal("a"), cty.StringVal("b"), cty.StringVal("c"),
		}),
	},
	{
		// ReverseListFunc preserves the input element type — a tuple
		// of strings stays a tuple in reversed order.
		Name: "reverse",
		Want: cty.TupleVal([]cty.Value{
			cty.StringVal("c"), cty.StringVal("b"), cty.StringVal("a"),
		}),
	},
	{
		// SliceFunc preserves the input element type; tuple slice
		// returns a tuple of the requested span.
		Name: "slice",
		Want: cty.TupleVal([]cty.Value{
			cty.StringVal("b"), cty.StringVal("c"), cty.StringVal("d"),
		}),
	},
	{
		// chunklist splits by size; trailing chunk is shorter.
		Name: "chunklist",
		Want: cty.ListVal([]cty.Value{
			cty.ListVal([]cty.Value{cty.StringVal("a"), cty.StringVal("b")}),
			cty.ListVal([]cty.Value{cty.StringVal("c"), cty.StringVal("d")}),
			cty.ListVal([]cty.Value{cty.StringVal("e")}),
		}),
	},
	{
		// compact filters empty strings out of the list-of-strings.
		Name: "compact",
		Want: cty.ListVal([]cty.Value{
			cty.StringVal("a"), cty.StringVal("b"), cty.StringVal("c"),
		}),
	},
	{Name: "coalesce", Want: cty.StringVal("first-non-empty")},
	{
		Name: "coalescelist",
		Want: cty.TupleVal([]cty.Value{cty.StringVal("first-non-empty")}),
	},
	{
		// zipmap produces an object (HCL string-key shape) with
		// per-key types from the corresponding values.
		Name: "zipmap",
		Want: cty.ObjectVal(map[string]cty.Value{
			"a": cty.NumberIntVal(1), "b": cty.NumberIntVal(2),
		}),
	},
	{
		// range(1, 4) → [1, 2, 3]; result is list(number).
		Name: "range",
		Want: cty.ListVal([]cty.Value{
			cty.NumberIntVal(1), cty.NumberIntVal(2), cty.NumberIntVal(3),
		}),
	},

	// Regex family — return-type shape dispatches on the pattern's
	// capture-group structure (no groups → string, unnamed → tuple,
	// named → object). Pin all three shapes so future cty upgrades
	// can't silently change the contract.
	{
		// No capture groups → returns the matched substring as a string.
		Name: "regex_no_groups",
		Want: cty.StringVal("abc"),
	},
	{
		// Unnamed groups → returns a tuple of the captured substrings,
		// in declaration order. The match itself ($0) is NOT included
		// when groups are present.
		Name: "regex_unnamed_groups",
		Want: cty.TupleVal([]cty.Value{
			cty.StringVal("abc"), cty.StringVal("123"),
		}),
	},
	{
		// Named groups → returns an object keyed by group name. Same
		// "no $0" rule as unnamed groups.
		Name: "regex_named_groups",
		Want: cty.ObjectVal(map[string]cty.Value{
			"word": cty.StringVal("abc"),
			"num":  cty.StringVal("123"),
		}),
	},
	{
		// regexall returns every non-overlapping match as a list (not
		// tuple) — element type is uniform.
		Name: "regexall",
		Want: cty.ListVal([]cty.Value{
			cty.StringVal("abc"), cty.StringVal("def"), cty.StringVal("ghi"),
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
		"upper", "lower", "title", "join", "split", "format", "formatlist",
		"replace", "trim", "trimspace", "trimprefix", "trimsuffix",
		"chomp", "indent", "substr",
		"sort", "reverse", "slice", "chunklist", "compact",
		"coalesce", "coalescelist", "zipmap", "range",
		"regex", "regexall",
		"abs", "min", "max", "floor", "ceil", "pow",
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

// TestEvalErrorCases exercises the error paths of the Terraform-side
// wrappers (replace.go, coalesce.go) by evaluating malformed inputs
// through Functions() and asserting the call surfaces diagnostics.
// Without this the negative branches go uncovered — the happy cases
// in TestStdlibFunctionCases only prove successful evaluation.
func TestEvalErrorCases(t *testing.T) {
	cases := []struct {
		Name string
		Src  string
	}{
		{
			// replace.go: regexp.Compile rejects unterminated character
			// classes — surfaced as evaluation diags.
			Name: "replace_invalid_regex",
			Src:  `locals { out = replace("abc", "/[unterminated/", "x") }`,
		},
		{
			// coalesce.go: type-unification fails when args are not
			// promotable to a common type (string vs map).
			Name: "coalesce_mismatched_types",
			Src:  `locals { out = coalesce("a", {b = 1}) }`,
		},
		{
			// coalesce.go: all args are empty strings → falls through
			// to the "no non-null, non-empty-string arguments" error.
			Name: "coalesce_all_empty",
			Src:  `locals { out = coalesce("", "", "") }`,
		},
		{
			// coalesce.go: explicit nulls plus an empty string take the
			// IsNull() branch (and then the all-empty error).
			Name: "coalesce_only_null_and_empty",
			Src:  `locals { out = coalesce(null, "") }`,
		},
		{
			// regex with no match: cty's RegexFunc errors out (matches
			// Terraform). Use regexall when "no match → empty" is the
			// desired behaviour.
			Name: "regex_no_match_errors",
			Src:  `locals { out = regex("[0-9]+", "no-digits-here") }`,
		},
		{
			// Invalid regex pattern — surfaces as an arg error from
			// regexp.Compile.
			Name: "regex_invalid_pattern",
			Src:  `locals { out = regex("[unterminated", "abc") }`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.Name, func(t *testing.T) {
			p := hclparse.NewParser()
			f, diags := p.ParseHCL([]byte(tc.Src), "main.tf")
			if diags.HasErrors() {
				t.Fatalf("parse: %v", diags)
			}
			body := f.Body.(*hclsyntax.Body)
			attr := body.Blocks[0].Body.Attributes["out"]
			ctx := &hcl.EvalContext{Functions: stdlib.Functions()}
			if _, evalDiags := attr.Expr.Value(ctx); !evalDiags.HasErrors() {
				t.Errorf("expected evaluation diagnostics, got none")
			}
		})
	}
}

// TestCoalesceUnknown exercises the IsKnown() short-circuit in
// coalesce.go: an unknown-typed argument causes the function to
// return UnknownVal rather than skipping or erroring. Driven through
// the Function directly because HCL literals are always known.
func TestCoalesceUnknown(t *testing.T) {
	got, err := stdlib.Functions()["coalesce"].Call([]cty.Value{
		cty.UnknownVal(cty.String),
		cty.StringVal("fallback"),
	})
	if err != nil {
		t.Fatalf("Call: %v", err)
	}
	if got.IsKnown() {
		t.Errorf("coalesce(unknown, ...) should return Unknown, got %#v", got)
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
