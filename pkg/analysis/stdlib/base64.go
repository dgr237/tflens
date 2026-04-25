package stdlib

import (
	"encoding/base64"
	"fmt"
	"unicode/utf8"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// terraformBase64EncodeFunc mirrors Terraform's `base64encode(string)`:
// returns the std-encoding base64 of the input string. Terraform exposes
// these in lang/funcs rather than cty, so the wrapper is needed.
var terraformBase64EncodeFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "str", Type: cty.String},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		return cty.StringVal(base64.StdEncoding.EncodeToString([]byte(args[0].AsString()))), nil
	},
})

// terraformBase64DecodeFunc mirrors Terraform's `base64decode(string)`:
// decodes std-encoding base64 to a UTF-8 string. Errors on invalid
// base64 (matches Terraform). Errors on non-UTF-8 output too — that
// covers binary payloads that aren't legal as a Terraform string,
// matching Terraform's "the result must be valid UTF-8" rule.
var terraformBase64DecodeFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "str", Type: cty.String},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		raw, err := base64.StdEncoding.DecodeString(args[0].AsString())
		if err != nil {
			return cty.UnknownVal(cty.String), fmt.Errorf("failed to decode base64: %w", err)
		}
		if !utf8.Valid(raw) {
			return cty.UnknownVal(cty.String), fmt.Errorf("the result of decoding the provided string is not valid UTF-8")
		}
		return cty.StringVal(string(raw)), nil
	},
})
