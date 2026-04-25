package analysis

import (
	"bytes"

	"github.com/zclconf/go-cty/cty"
	ctyjson "github.com/zclconf/go-cty/cty/json"
)

// ValueEquivalent reports whether old and new are the same value
// after ignoring the cty Tuple↔List↔Set distinction. cty's
// RawEquals is type-strict — a literal `["a","b"]` (Tuple) and
// `distinct(["a","b"])` (List of String) compare not-equal under
// RawEquals, even though both produce identical for_each / count
// expansions in Terraform and identical downstream behaviour.
//
// JSON marshalling collapses the type distinction: tuple, list, and
// set all serialise to the same JSON-array shape, and object/map to
// the same JSON-object shape. Byte-equal JSON means the values
// would behave identically downstream. Genuine differences (numeric
// vs string, different elements, different keys) still serialise
// differently and so still flag.
//
// Used by pkg/diff (tracked-attribute effective-value collapse) and
// pkg/statediff (sensitive-local effective-value collapse) when the
// new function-evaluation surface produces values whose cty types
// don't line up exactly across two sides of a comparison.
//
// On a marshal error (defensive — shouldn't happen for well-typed
// values from a successful Eval), falls back to RawEquals.
func ValueEquivalent(old, neu cty.Value) bool {
	if old.RawEquals(neu) {
		return true
	}
	oldJSON, err := ctyjson.Marshal(old, old.Type())
	if err != nil {
		return false
	}
	newJSON, err := ctyjson.Marshal(neu, neu.Type())
	if err != nil {
		return false
	}
	return bytes.Equal(oldJSON, newJSON)
}
