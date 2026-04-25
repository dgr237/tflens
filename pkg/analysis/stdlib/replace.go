package stdlib

import (
	"regexp"
	"strings"

	"github.com/zclconf/go-cty/cty"
	"github.com/zclconf/go-cty/cty/function"
)

// terraformReplaceFunc mirrors Terraform's `replace(str, substr, replace)`,
// which dispatches on whether `substr` is a `/regex/`-delimited pattern.
// cty's stdlib.ReplaceFunc is literal-only, so wrapping it directly would
// silently produce wrong results for the regex form.
var terraformReplaceFunc = function.New(&function.Spec{
	Params: []function.Parameter{
		{Name: "str", Type: cty.String},
		{Name: "substr", Type: cty.String},
		{Name: "replace", Type: cty.String},
	},
	Type: function.StaticReturnType(cty.String),
	Impl: func(args []cty.Value, _ cty.Type) (cty.Value, error) {
		str := args[0].AsString()
		substr := args[1].AsString()
		replace := args[2].AsString()
		if len(substr) > 1 && substr[0] == '/' && substr[len(substr)-1] == '/' {
			re, err := regexp.Compile(substr[1 : len(substr)-1])
			if err != nil {
				return cty.UnknownVal(cty.String), err
			}
			return cty.StringVal(re.ReplaceAllString(str, replace)), nil
		}
		return cty.StringVal(strings.ReplaceAll(str, substr, replace)), nil
	},
})
