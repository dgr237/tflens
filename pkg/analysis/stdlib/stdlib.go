// Package stdlib registers the Terraform-language built-in functions
// that pkg/analysis wires into Module.EvalContext for static
// expression evaluation.
//
// The set is intentionally a curated subset (NOT every Terraform
// built-in) — see the project's CLAUDE.md for the rationale on
// what's in vs. out. Adding a new batch of functions is a single
// edit to Functions().
//
// All implementations come from cty/function/stdlib, which is the
// same library Terraform itself uses for these specific functions.
// Wrapping them here means our evaluation behaviour matches
// Terraform's exactly for the functions we cover. Functions that
// would need filesystem access (file, fileset, templatefile),
// non-deterministic state (timestamp, uuid, bcrypt), or full
// evaluator catch-and-retry semantics (can, try) are intentionally
// excluded so the curated set stays evaluation-pure.
package stdlib

import (
	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
)

// Functions returns the curated Terraform-stdlib function set keyed
// by the lowercase Terraform-source name (e.g. "length", "toset").
// Suitable for plugging into hcl.EvalContext{Functions: ...}.
//
// A fresh map is constructed per call so callers may mutate the
// returned value (e.g. to swap an implementation in tests) without
// affecting other callers.
func Functions() map[string]function.Function {
	return map[string]function.Function{
		// Type conversion — pure type-system bridges.
		"toset":    stdlib.MakeToFunc(cty.Set(cty.DynamicPseudoType)),
		"tolist":   stdlib.MakeToFunc(cty.List(cty.DynamicPseudoType)),
		"tomap":    stdlib.MakeToFunc(cty.Map(cty.DynamicPseudoType)),
		"tostring": stdlib.MakeToFunc(cty.String),
		"tonumber": stdlib.MakeToFunc(cty.Number),
		"tobool":   stdlib.MakeToFunc(cty.Bool),

		// Core collection operations.
		"length":   stdlib.LengthFunc,
		"concat":   stdlib.ConcatFunc,
		"merge":    stdlib.MergeFunc,
		"keys":     stdlib.KeysFunc,
		"values":   stdlib.ValuesFunc,
		"lookup":   stdlib.LookupFunc,
		"contains": stdlib.ContainsFunc,
		"element":  stdlib.ElementFunc,
		"flatten":  stdlib.FlattenFunc,
		"distinct": stdlib.DistinctFunc,
	}
}
