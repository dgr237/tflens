// Package stdlib registers the Terraform-language built-in functions
// that pkg/analysis wires into Module.EvalContext for static
// expression evaluation.
//
// The set is intentionally a curated subset (NOT every Terraform
// built-in) — see the project's CLAUDE.md for the rationale on
// what's in vs. out. Adding a new batch of functions is a single
// edit to Functions().
//
// Most implementations come from cty/function/stdlib, which is the
// same library Terraform itself uses for these specific functions.
// Wrapping them here means our evaluation behaviour matches
// Terraform's exactly for the functions we cover. A handful of
// functions need Terraform-side wrappers because they diverge from
// cty's defaults or aren't in cty at all:
//
//   - replace.go  — Terraform dispatches `/regex/` to a regex impl;
//     cty's ReplaceFunc is literal-only.
//   - coalesce.go — Terraform skips empty strings, not just nulls.
//   - index.go    — cty's IndexFunc returns the element AT a given
//     key; Terraform's `index` returns the position of a value.
//   - base64.go   — cty doesn't expose base64 encode/decode at all
//     (Terraform implements them in lang/funcs).
//
// Functions that would need filesystem access (file, fileset,
// templatefile), non-deterministic state (timestamp, uuid, bcrypt),
// or full evaluator catch-and-retry semantics (can, try) are
// intentionally excluded so the curated set stays evaluation-pure.
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

		// String functions. `replace` needs a Terraform-side wrapper
		// because the literal-vs-regex dispatch (`/pattern/` triggers
		// regex mode) lives above cty's literal-only ReplaceFunc.
		"upper":      stdlib.UpperFunc,
		"lower":      stdlib.LowerFunc,
		"title":      stdlib.TitleFunc,
		"join":       stdlib.JoinFunc,
		"split":      stdlib.SplitFunc,
		"format":     stdlib.FormatFunc,
		"formatlist": stdlib.FormatListFunc,
		"replace":    terraformReplaceFunc,
		"trim":       stdlib.TrimFunc,
		"trimspace":  stdlib.TrimSpaceFunc,
		"trimprefix": stdlib.TrimPrefixFunc,
		"trimsuffix": stdlib.TrimSuffixFunc,
		"chomp":      stdlib.ChompFunc,
		"indent":     stdlib.IndentFunc,
		"substr":     stdlib.SubstrFunc,

		// Regex family. cty's implementations use Go's regexp (RE2),
		// the same engine Terraform uses, so behaviour matches directly
		// without a wrapper. Capture-group return-type shape (string /
		// tuple / object) is dispatched on the pattern at evaluation
		// time — see the corresponding testdata fixtures. Note: cty has
		// a `RegexReplaceFunc` but Terraform doesn't expose it as a
		// distinct function — regex replacement happens via the
		// `/pattern/` form of `replace` (handled by replace.go's
		// dispatcher). Wiring `regexreplace` here would diverge from
		// Terraform.
		"regex":    stdlib.RegexFunc,
		"regexall": stdlib.RegexAllFunc,

		// Encoders / decoders. JSON round-trip in particular powers a
		// lot of real-world value-collapse — IAM policies and configs
		// often switch between an object literal + jsonencode() and a
		// raw JSON string. base64 covers cloud-init userdata and
		// similar binary payloads. The hashing/compression base64*
		// variants (base64gzip, base64sha256, base64sha512) need
		// crypto state that isn't pure-functional and are out.
		"jsondecode":   stdlib.JSONDecodeFunc,
		"jsonencode":   stdlib.JSONEncodeFunc,
		"csvdecode":    stdlib.CSVDecodeFunc,
		"base64encode": terraformBase64EncodeFunc,
		"base64decode": terraformBase64DecodeFunc,

		// Set-algebra helpers. Common in for_each composition where
		// the iteration set is built from multiple inputs.
		"setunion":               stdlib.SetUnionFunc,
		"setintersection":        stdlib.SetIntersectionFunc,
		"setsubtract":            stdlib.SetSubtractFunc,
		"setsymmetricdifference": stdlib.SetSymmetricDifferenceFunc,
		"setproduct":             stdlib.SetProductFunc,

		// List + numeric pickups. `index` needs a Terraform-side
		// wrapper because cty's IndexFunc is a different function
		// (returns the element AT a given key); see index.go.
		"index":    terraformIndexFunc,
		"parseint": stdlib.ParseIntFunc,

		// Additional collection helpers — same value-collapse story as
		// the batch-1 collection functions; these are the next tier of
		// commonly-used Terraform-stdlib pure functions.
		"sort":         stdlib.SortFunc,
		"reverse":      stdlib.ReverseListFunc,
		"slice":        stdlib.SliceFunc,
		"chunklist":    stdlib.ChunklistFunc,
		"compact":      stdlib.CompactFunc,
		"coalesce":     terraformCoalesceFunc,
		"coalescelist": stdlib.CoalesceListFunc,
		"zipmap":       stdlib.ZipmapFunc,
		"range":        stdlib.RangeFunc,

		// Numeric functions.
		"abs":   stdlib.AbsoluteFunc,
		"min":   stdlib.MinFunc,
		"max":   stdlib.MaxFunc,
		"floor": stdlib.FloorFunc,
		"ceil":  stdlib.CeilFunc,
		"pow":   stdlib.PowFunc,
	}
}
