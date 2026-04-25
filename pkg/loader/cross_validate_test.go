package loader_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/loader"
)

// crossValidateCase describes one TestCrossValidateCases case.
//
// For the standard parent-child shape, fixtures are read from
// pkg/loader/testdata/cross_validate/<Name>/parent.tf and
// pkg/loader/testdata/cross_validate/<Name>/child/main.tf. The case can
// then assert that:
//   - WantNoErrors: no validation errors are emitted.
//   - WantMsgContains: at least one error has all of these substrings in Msg.
//   - WantEntityIDAndMsg: at least one error matches both fields (entityID
//     wildcards as ""; msgContains substrings must all appear).
//   - MustNotContainMsg: no error may contain ANY of these substrings.
//
// Cases that need richer setup (multi-module, project-level overrides,
// nested children) provide Custom and ignore the parent/child fixtures.
type crossValidateCase struct {
	Name string

	WantNoErrors       bool
	WantMsgContains    []string
	WantEntityIDAndMsg *struct {
		EntityID    string
		MsgContains []string
	}
	MustNotContainMsg []string

	Custom func(t *testing.T)
}

func TestCrossValidateCases(t *testing.T) {
	for _, tc := range crossValidateCases {
		t.Run(tc.Name, func(t *testing.T) {
			if tc.Custom != nil {
				tc.Custom(t)
				return
			}
			parent := loadCrossValidateFixture(t, tc.Name, "parent.tf")
			child := loadCrossValidateFixture(t, tc.Name, "child/main.tf")
			errs := runCrossValidate(t, parent, child)

			if tc.WantNoErrors {
				if len(errs) != 0 {
					t.Errorf("expected no errors, got: %v", errs)
				}
				return
			}
			if len(tc.WantMsgContains) > 0 {
				if !anyErrMsg(errs, tc.WantMsgContains) {
					t.Errorf("expected error containing %v, got: %v",
						tc.WantMsgContains, errs)
				}
			}
			if tc.WantEntityIDAndMsg != nil {
				if !anyErrEntityIDAndMsg(errs, tc.WantEntityIDAndMsg.EntityID, tc.WantEntityIDAndMsg.MsgContains) {
					t.Errorf("expected error EntityID=%q containing %v, got: %v",
						tc.WantEntityIDAndMsg.EntityID, tc.WantEntityIDAndMsg.MsgContains, errs)
				}
			}
			for _, e := range errs {
				for _, banned := range tc.MustNotContainMsg {
					if strings.Contains(e.Msg, banned) {
						t.Errorf("error should not contain %q: %v", banned, e)
					}
				}
			}
		})
	}
}

// loadCrossValidateFixture reads pkg/loader/testdata/cross_validate/<dir>/<file>.
// Returns "" when the file doesn't exist.
func loadCrossValidateFixture(t *testing.T, dir, file string) string {
	t.Helper()
	path := filepath.Join("testdata", "cross_validate", dir, file)
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return ""
		}
		t.Fatalf("read %s: %v", path, err)
	}
	return string(b)
}

func anyErrMsg(errs []analysis.ValidationError, contains []string) bool {
	for _, e := range errs {
		ok := true
		for _, s := range contains {
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

func anyErrEntityIDAndMsg(errs []analysis.ValidationError, entityID string, msgContains []string) bool {
	for _, e := range errs {
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

// miniProject creates a tmp dir with a parent main.tf and a child module
// under ./child/main.tf. Returns the parent directory path.
func miniProject(t *testing.T, parentSrc, childSrc string) string {
	t.Helper()
	root := t.TempDir()
	childDir := filepath.Join(root, "child")
	if err := os.MkdirAll(childDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTF(t, root, "main.tf", parentSrc)
	writeTF(t, childDir, "main.tf", childSrc)
	return root
}

func runCrossValidate(t *testing.T, parentSrc, childSrc string) []analysis.ValidationError {
	t.Helper()
	root := miniProject(t, parentSrc, childSrc)
	proj, fileErrs, err := loader.LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	for _, fe := range fileErrs {
		t.Logf("parse warning: %s", fe.Error())
	}
	return loader.CrossValidate(proj)
}

var crossValidateCases = []crossValidateCase{
	{
		Name: "required_input_missing",
		WantEntityIDAndMsg: &struct {
			EntityID    string
			MsgContains []string
		}{"module.net", []string{"required input", "cidr"}},
	},
	{Name: "optional_input_not_required", WantNoErrors: true},
	{Name: "all_inputs_provided", WantNoErrors: true},
	{
		Name: "unknown_argument",
		WantEntityIDAndMsg: &struct {
			EntityID    string
			MsgContains []string
		}{"module.net", []string{"unknown argument", "typo"}},
	},
	{
		Name: "type_mismatch",
		WantEntityIDAndMsg: &struct {
			EntityID    string
			MsgContains []string
		}{"module.net", []string{"number", "string"}},
	},
	{Name: "compatible_type_no_error", WantNoErrors: true},
	{Name: "any_accepts_anything", WantNoErrors: true},
	{Name: "var_reference_uses_declared_type", WantNoErrors: true},
	{
		// Regression: `var.config.property` where `var.config` has type
		// object({property = number}) used to be inferred as object(...)
		// because the type-inference path stopped at parts[1] and ignored
		// the trailing `.property` traversal. Should infer as number.
		Name:         "var_object_field_traversal",
		WantNoErrors: true,
	},
	{
		// Two-hop traversal: var.config.network.cidr where config is
		// object({network = object({cidr = string})}). Should resolve
		// to string and pass cross-validation against a string variable.
		Name:         "var_object_field_nested",
		WantNoErrors: true,
	},
	{
		// `var.config.notdeclared` against object({property = number}):
		// the field doesn't exist, so we cannot infer a type. Must NOT
		// false-positive against the parent's object type — better to
		// skip the type check than emit a wrong-type complaint.
		Name:              "var_object_field_unknown_skipped",
		MustNotContainMsg: []string{"but child variable expects"},
	},
	{
		// `var.config.name` resolves to string; child wants number.
		// This is a real type mismatch and must still be flagged.
		Name: "var_object_field_type_mismatch",
		WantEntityIDAndMsg: &struct {
			EntityID    string
			MsgContains []string
		}{"module.net", []string{"string", "number"}},
	},
	{
		Name: "var_reference_type_mismatch",
		WantEntityIDAndMsg: &struct {
			EntityID    string
			MsgContains []string
		}{"module.net", []string{"string", "number"}},
	},
	{
		Name:              "unknown_expr_skipped",
		MustNotContainMsg: []string{"but child variable expects"},
	},
	{
		Name:              "child_with_no_type_constraint_skips_typecheck",
		MustNotContainMsg: []string{"but child variable expects"},
	},
	{
		Name:         "output_reference_satisfied",
		WantNoErrors: true,
	},
	{
		Name: "output_reference_missing",
		WantEntityIDAndMsg: &struct {
			EntityID    string
			MsgContains []string
		}{"module.net", []string{"references module.net.gone", "no such output"}},
	},

	// ---- richer setups via Custom ----

	{
		Name: "remote_source_skipped",
		Custom: func(t *testing.T) {
			root := t.TempDir()
			writeTF(t, root, "main.tf",
				"module \"net\" { source = \"hashicorp/network/aws\", version = \"1.0.0\" }\n")
			proj, _, err := loader.LoadProject(root)
			if err != nil {
				t.Fatalf("LoadProject: %v", err)
			}
			if errs := loader.CrossValidate(proj); len(errs) != 0 {
				t.Errorf("remote module should be silently skipped, got: %v", errs)
			}
		},
	},
	{
		Name: "call_focuses_on_named_module",
		Custom: func(t *testing.T) {
			root := t.TempDir()
			for _, sub := range []string{"a", "b"} {
				if err := os.MkdirAll(filepath.Join(root, sub), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			writeTF(t, root, "main.tf", `
module "a" { source = "./a" }
module "b" { source = "./b" }
`)
			writeTF(t, filepath.Join(root, "a"), "main.tf",
				"variable \"need_a\" { type = string }\n")
			writeTF(t, filepath.Join(root, "b"), "main.tf",
				"variable \"need_b\" { type = string }\n")

			proj, _, err := loader.LoadProject(root)
			if err != nil {
				t.Fatalf("LoadProject: %v", err)
			}
			errs := loader.CrossValidateCall(proj.Root.Module, "a", proj.Root.Children["a"].Module)
			if len(errs) == 0 {
				t.Fatal("expected error for missing need_a")
			}
			for _, e := range errs {
				if strings.Contains(e.Msg, "need_b") {
					t.Errorf("scoped check should not surface need_b issues: %v", e)
				}
			}
		},
	},
	{
		Name: "call_unknown_module_returns_nil",
		Custom: func(t *testing.T) {
			root := t.TempDir()
			writeTF(t, root, "main.tf", `variable "x" {}`)
			proj, _, err := loader.LoadProject(root)
			if err != nil {
				t.Fatalf("LoadProject: %v", err)
			}
			errs := loader.CrossValidateCall(proj.Root.Module, "nope", proj.Root.Module)
			if errs != nil {
				t.Errorf("expected nil for unknown module call, got: %v", errs)
			}
		},
	},
	{
		Name: "call_against_candidate_version",
		Custom: func(t *testing.T) {
			root := t.TempDir()
			childV1 := filepath.Join(root, "child-v1")
			childV2 := t.TempDir()
			if err := os.MkdirAll(childV1, 0o755); err != nil {
				t.Fatal(err)
			}
			writeTF(t, root, "main.tf", `
module "vpc" {
  source = "./child-v1"
  cidr   = "10.0.0.0/16"
}
`)
			writeTF(t, childV1, "variables.tf",
				"variable \"cidr\" { type = string }\n")
			writeTF(t, childV2, "variables.tf",
				"variable \"cidr\" { type = string }\nvariable \"region\" { type = string }\n")

			proj, _, err := loader.LoadProject(root)
			if err != nil {
				t.Fatalf("LoadProject: %v", err)
			}
			if errs := loader.CrossValidateCall(proj.Root.Module, "vpc", proj.Root.Children["vpc"].Module); len(errs) != 0 {
				t.Errorf("parent should be compatible with v1, got: %v", errs)
			}
			v2Mod, _, err := loader.LoadDir(childV2)
			if err != nil {
				t.Fatalf("LoadDir v2: %v", err)
			}
			errs := loader.CrossValidateCall(proj.Root.Module, "vpc", v2Mod)
			found := false
			for _, e := range errs {
				if strings.Contains(e.Msg, "region") && strings.Contains(e.Msg, "required input") {
					found = true
				}
			}
			if !found {
				t.Errorf("expected v2 to flag missing region, got: %v", errs)
			}
		},
	},
	{
		Name: "transitive",
		Custom: func(t *testing.T) {
			root := t.TempDir()
			mid := filepath.Join(root, "mid")
			leaf := filepath.Join(mid, "leaf")
			if err := os.MkdirAll(leaf, 0o755); err != nil {
				t.Fatal(err)
			}
			writeTF(t, root, "main.tf", "module \"mid\" { source = \"./mid\" }\n")
			writeTF(t, mid, "main.tf", "module \"leaf\" { source = \"./leaf\" }\n")
			writeTF(t, leaf, "main.tf", "variable \"required\" { type = string }\n")

			proj, _, err := loader.LoadProject(root)
			if err != nil {
				t.Fatalf("LoadProject: %v", err)
			}
			errs := loader.CrossValidate(proj)
			found := false
			for _, e := range errs {
				if e.EntityID == "module.leaf" && strings.Contains(e.Msg, "required input") {
					found = true
				}
			}
			if !found {
				t.Errorf("expected transitive error reaching leaf, got: %v", errs)
			}
		},
	},
}
