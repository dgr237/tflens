package stdlib

import (
	"errors"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/convert"
	"github.com/zclconf/go-cty/cty/function"
)

// terraformCoalesceFunc mirrors Terraform's `coalesce(...)`, which
// returns the first argument that is neither null NOR (for strings)
// an empty string. cty's stdlib.CoalesceFunc only skips nulls, so a
// direct wrap would diverge for the very common
// `coalesce("", var.fallback)` idiom.
var terraformCoalesceFunc = function.New(&function.Spec{
	Params: []function.Parameter{},
	VarParam: &function.Parameter{
		Name:             "vals",
		Type:             cty.DynamicPseudoType,
		AllowUnknown:     true,
		AllowDynamicType: true,
		AllowNull:        true,
	},
	Type: func(args []cty.Value) (cty.Type, error) {
		argTypes := make([]cty.Type, len(args))
		for i, v := range args {
			argTypes[i] = v.Type()
		}
		retType, _ := convert.UnifyUnsafe(argTypes)
		if retType == cty.NilType {
			return cty.NilType, errors.New("all arguments must have the same type")
		}
		return retType, nil
	},
	Impl: func(args []cty.Value, retType cty.Type) (cty.Value, error) {
		for _, v := range args {
			if !v.IsKnown() {
				return cty.UnknownVal(retType), nil
			}
			if v.IsNull() {
				continue
			}
			if retType == cty.String && v.Type() == cty.String && v.AsString() == "" {
				continue
			}
			return convert.Convert(v, retType)
		}
		return cty.NilVal, errors.New("no non-null, non-empty-string arguments")
	},
})
