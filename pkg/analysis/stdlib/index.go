package stdlib

import (
	"errors"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
	"github.com/zclconf/go-cty/cty/function/stdlib"
)

// terraformIndexFunc mirrors Terraform's `index(list, value)`, which
// returns the position of the first occurrence of `value` in `list`.
// cty's stdlib.IndexFunc is a different function (it returns the
// element AT a given key), so wiring cty's directly would silently
// give the wrong answer for `index([…], "x")` — wrong both in shape
// (returns the value, not the position) and in error semantics.
var terraformIndexFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "list", Type: cty.DynamicPseudoType},
		{Name: "value", Type: cty.DynamicPseudoType, AllowDynamicType: true},
	},
	Type: function.StaticReturnType(cty.Number),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		// The function machinery (no AllowUnknown on the list param)
		// already short-circuits on unknown args before we get here,
		// so no IsKnown() guard is needed in the Impl. Likewise, type-
		// mismatched Equal calls (e.g. comparing a string against a
		// number element) return cty.False rather than an error, so no
		// err-skip path is reachable through normal evaluation.
		if !(args[0].Type().IsListType() || args[0].Type().IsTupleType()) {
			return cty.NilVal, errors.New("argument must be a list or tuple")
		}
		if args[0].LengthInt() == 0 {
			return cty.NilVal, errors.New("cannot search an empty list")
		}
		for it := args[0].ElementIterator(); it.Next(); {
			i, v := it.Element()
			eq, _ := stdlib.Equal(v, args[1])
			if !eq.IsKnown() {
				return cty.UnknownVal(cty.Number), nil
			}
			if eq.True() {
				return i, nil
			}
		}
		return cty.NilVal, errors.New("item not found")
	},
})
