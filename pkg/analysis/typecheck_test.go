package analysis_test

import (
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
)

// typecheckCase describes one case for TestTypecheckCases. The fixture is
// read from pkg/analysis/testdata/typecheck/<Name>/main.tf.
//
// Either WantNoErrors must be set (asserts TypeErrors() is empty), or one
// of the assertion fields below must match at least one error in the result:
//
//   - WantErrorAttr: an error has this Attr ("default", "for_each", "count").
//   - WantErrorEntityID: an error has this EntityID.
//   - WantErrorMsgContains: an error has all of these substrings in Msg.
//
// For DeclaredType assertions (no errors expected, just a type lookup), set
// Subject (e.g. "variable.x") and WantDeclaredType (e.g. "list(string)").
//
// Custom is the escape hatch for cases that need richer assertions (sort
// order, multi-error counts, etc.).
type typecheckCase struct {
	Name string

	WantNoErrors         bool
	WantErrorAttr        string
	WantErrorEntityID    string
	WantErrorMsgContains []string

	Subject          string // entity ID for DeclaredType lookup
	WantDeclaredType string

	Custom func(t *testing.T, m *analysis.Module)
}

func TestTypecheckCases(t *testing.T) {
	for _, tc := range typecheckCases {
		t.Run(tc.Name, func(t *testing.T) {
			src := loadAnalysisFixture(t, "typecheck", tc.Name)
			m := analyseFixture(t, src)

			if tc.Custom != nil {
				tc.Custom(t, m)
				return
			}

			if tc.Subject != "" {
				assertDeclaredType(t, m, tc.Subject, tc.WantDeclaredType)
			}

			errs := m.TypeErrors()
			if tc.WantNoErrors {
				if len(errs) != 0 {
					t.Errorf("expected no type errors, got: %v", errs)
				}
				return
			}

			if tc.WantErrorAttr == "" && tc.WantErrorEntityID == "" && tc.WantErrorMsgContains == nil && tc.Subject == "" {
				return
			}
			if tc.WantErrorAttr == "" && tc.WantErrorEntityID == "" && tc.WantErrorMsgContains == nil {
				// DeclaredType-only case; no error assertion needed.
				return
			}
			if !anyErrorMatches(errs, tc.WantErrorAttr, tc.WantErrorEntityID, tc.WantErrorMsgContains) {
				t.Errorf("no type error matched (attr=%q entityID=%q msg~%v); got: %v",
					tc.WantErrorAttr, tc.WantErrorEntityID, tc.WantErrorMsgContains, errs)
			}
		})
	}
}

func assertDeclaredType(t *testing.T, m *analysis.Module, subject, want string) {
	t.Helper()
	for _, e := range m.Entities() {
		if e.ID() == subject {
			got := "<nil>"
			if e.DeclaredType != nil {
				got = e.DeclaredType.String()
			}
			if got != want {
				t.Errorf("DeclaredType for %s = %q, want %q", subject, got, want)
			}
			return
		}
	}
	t.Fatalf("entity %s not found", subject)
}

func anyErrorMatches(errs []analysis.TypeCheckError, attr, entityID string, msgContains []string) bool {
	for _, e := range errs {
		if attr != "" && e.Attr != attr {
			continue
		}
		if entityID != "" && e.EntityID != entityID {
			continue
		}
		ok := true
		for _, s := range msgContains {
			if !strings.Contains(e.Msg, s) {
				ok = false
				break
			}
		}
		if ok {
			return true
		}
	}
	return false
}

var typecheckCases = []typecheckCase{
	// ---- ParseType ----
	{Name: "primitive_string", Subject: "variable.x", WantDeclaredType: "string"},
	{Name: "primitive_number", Subject: "variable.x", WantDeclaredType: "number"},
	{Name: "primitive_bool", Subject: "variable.x", WantDeclaredType: "bool"},
	{Name: "primitive_any", Subject: "variable.x", WantDeclaredType: "any"},
	{Name: "parameterised_list", Subject: "variable.names", WantDeclaredType: "list(string)"},
	{Name: "parameterised_map", Subject: "variable.tags", WantDeclaredType: "map(string)"},
	{Name: "parameterised_set", Subject: "variable.ids", WantDeclaredType: "set(number)"},
	{
		Name: "object_type",
		Custom: func(t *testing.T, m *analysis.Module) {
			v := m.Filter(analysis.KindVariable)[0]
			if v.DeclaredType.Kind != analysis.TypeObject {
				t.Fatalf("kind = %v, want Object", v.DeclaredType.Kind)
			}
			if v.DeclaredType.Fields["name"].Kind != analysis.TypeString {
				t.Errorf("name field: got %v, want string", v.DeclaredType.Fields["name"])
			}
			if v.DeclaredType.Fields["port"].Kind != analysis.TypeNumber {
				t.Errorf("port field: got %v, want number", v.DeclaredType.Fields["port"])
			}
		},
	},

	// ---- default value checking ----
	{Name: "default_matches_type", WantNoErrors: true},
	{
		Name: "default_type_mismatch",
		WantErrorAttr: "default",
		WantErrorMsgContains: []string{"variable.count", "string", "number"},
	},
	{Name: "default_null_always_ok", WantNoErrors: true},
	{Name: "default_any_accepts_anything", WantNoErrors: true},

	// ---- for_each ----
	{
		Name: "for_each_list_literal_fails",
		WantErrorAttr: "for_each",
		WantErrorMsgContains: []string{"map", "set"},
	},
	{Name: "for_each_map_literal_ok", WantNoErrors: true},
	{Name: "for_each_toset_ok", WantNoErrors: true},
	{
		Name: "for_each_list_var_fails",
		WantErrorMsgContains: []string{"list"},
	},
	{Name: "for_each_map_var_ok", WantNoErrors: true},
	{Name: "for_each_string_literal_fails"}, // any error suffices
	{
		Name: "for_each_in_module_block",
		WantErrorEntityID: "module.envs",
	},

	// ---- count ----
	{Name: "count_with_number_ok", WantNoErrors: true},
	{
		Name: "count_with_list_fails",
		WantErrorAttr: "count",
	},
	{Name: "count_with_bool_fails"}, // any error suffices
	{Name: "count_with_number_var_ok", WantNoErrors: true},

	// ---- edge cases ----
	{
		Name: "type_errors_sorted_by_position",
		Custom: func(t *testing.T, m *analysis.Module) {
			errs := m.TypeErrors()
			if len(errs) < 2 {
				t.Fatalf("expected 2 errors, got %d", len(errs))
			}
			if errs[0].Pos.Line >= errs[1].Pos.Line {
				t.Errorf("errors not sorted: got lines %d, %d", errs[0].Pos.Line, errs[1].Pos.Line)
			}
		},
	},
	{Name: "unknown_type_constraint_tolerated", WantNoErrors: true},
	{Name: "for_each_unknown_type_allowed", WantNoErrors: true},

	// ---- built-in function return types ----
	{
		Name: "for_each_with_keys_fails",
		WantErrorMsgContains: []string{"list"},
	},
	{Name: "for_each_with_values_fails"},
	{Name: "for_each_with_concat_fails"},
	{Name: "for_each_with_merge_ok", WantNoErrors: true},
	{Name: "for_each_with_fileset_ok", WantNoErrors: true},
	{Name: "count_with_length_ok", WantNoErrors: true},
	{Name: "count_with_keys_fails"},
	{Name: "default_with_jsonencode_ok", WantNoErrors: true},
	{
		Name: "default_with_wrong_builtin_fails",
		WantErrorMsgContains: []string{"number", "string"},
	},
}
