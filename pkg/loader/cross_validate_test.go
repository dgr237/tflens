package loader_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/loader"
)

// miniProject creates a tmp dir with a parent main.tf and a child module
// under ./child/main.tf. Returns the parent directory path.
func miniProject(t *testing.T, parentSrc, childSrc string) string {
	t.Helper()
	root := t.TempDir()
	childDir := filepath.Join(root, "child")
	if err := os.MkdirAll(childDir, 0755); err != nil {
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

// ---- missing required inputs ----

func TestCrossValidateRequiredInputMissing(t *testing.T) {
	parent := `module "net" { source = "./child" }`
	child := "variable \"cidr\" { type = string }\n"
	errs := runCrossValidate(t, parent, child)
	if len(errs) == 0 {
		t.Fatal("expected error for missing required input, got none")
	}
	found := false
	for _, e := range errs {
		if e.EntityID == "module.net" && strings.Contains(e.Msg, "required input") && strings.Contains(e.Msg, "cidr") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected error referencing required cidr, got: %v", errs)
	}
}

func TestCrossValidateOptionalInputNotRequired(t *testing.T) {
	// Child variable has a default → optional → parent doesn't have to pass it.
	parent := `module "net" { source = "./child" }`
	child := "variable \"cidr\" {\n  type    = string\n  default = \"10.0.0.0/16\"\n}\n"
	errs := runCrossValidate(t, parent, child)
	if len(errs) != 0 {
		t.Errorf("optional child variable should not trigger error, got: %v", errs)
	}
}

func TestCrossValidateAllInputsProvided(t *testing.T) {
	parent := "module \"net\" {\n  source = \"./child\"\n  cidr   = \"10.0.0.0/16\"\n  env    = \"dev\"\n}\n"
	child := "variable \"cidr\" { type = string }\nvariable \"env\" { type = string }\n"
	errs := runCrossValidate(t, parent, child)
	if len(errs) != 0 {
		t.Errorf("all required inputs passed — expected no errors, got: %v", errs)
	}
}

// ---- unknown arguments ----

func TestCrossValidateUnknownArgumentIsFlagged(t *testing.T) {
	parent := "module \"net\" {\n  source = \"./child\"\n  cidr   = \"10.0.0.0/16\"\n  typo   = \"surprise\"\n}\n"
	child := "variable \"cidr\" { type = string }\n"
	errs := runCrossValidate(t, parent, child)
	found := false
	for _, e := range errs {
		if e.EntityID == "module.net" && strings.Contains(e.Msg, "unknown argument") && strings.Contains(e.Msg, "typo") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected unknown-argument error for typo, got: %v", errs)
	}
}

// ---- type compatibility ----

func TestCrossValidateTypeMismatchIsFlagged(t *testing.T) {
	// Parent passes a number literal where child expects string.
	parent := "module \"net\" {\n  source = \"./child\"\n  cidr   = 42\n}\n"
	child := "variable \"cidr\" { type = string }\n"
	errs := runCrossValidate(t, parent, child)
	found := false
	for _, e := range errs {
		if e.EntityID == "module.net" && strings.Contains(e.Msg, "number") && strings.Contains(e.Msg, "string") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected type-mismatch error, got: %v", errs)
	}
}

func TestCrossValidateCompatibleTypeNoError(t *testing.T) {
	parent := "module \"net\" {\n  source    = \"./child\"\n  instances = 3\n  name      = \"app\"\n}\n"
	child := "variable \"instances\" { type = number }\nvariable \"name\" { type = string }\n"
	errs := runCrossValidate(t, parent, child)
	if len(errs) != 0 {
		t.Errorf("expected no errors, got: %v", errs)
	}
}

func TestCrossValidateAnyAcceptsAnything(t *testing.T) {
	// Child declares `any` → any parent value is acceptable.
	parent := "module \"net\" {\n  source = \"./child\"\n  cfg    = { a = 1, b = 2 }\n}\n"
	child := "variable \"cfg\" { type = any }\n"
	errs := runCrossValidate(t, parent, child)
	if len(errs) != 0 {
		t.Errorf("any should accept anything, got: %v", errs)
	}
}

func TestCrossValidateVarReferenceUsesDeclaredType(t *testing.T) {
	// Parent's arg is var.env where var.env has a declared type.
	parent := "variable \"env\" { type = string }\nmodule \"net\" {\n  source = \"./child\"\n  env    = var.env\n}\n"
	child := "variable \"env\" { type = string }\n"
	errs := runCrossValidate(t, parent, child)
	if len(errs) != 0 {
		t.Errorf("var ref with matching type should be clean, got: %v", errs)
	}
}

func TestCrossValidateVarReferenceTypeMismatch(t *testing.T) {
	// Parent passes var.env (string) to child variable typed as number.
	parent := "variable \"env\" { type = string }\nmodule \"net\" {\n  source    = \"./child\"\n  instances = var.env\n}\n"
	child := "variable \"instances\" { type = number }\n"
	errs := runCrossValidate(t, parent, child)
	found := false
	for _, e := range errs {
		if e.EntityID == "module.net" && strings.Contains(e.Msg, "string") && strings.Contains(e.Msg, "number") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected type mismatch for var.env → number, got: %v", errs)
	}
}

// ---- unknowable / ignorable cases ----

func TestCrossValidateUnknownExprSkipped(t *testing.T) {
	// Parent passes a resource attribute reference — type unknown at this level.
	parent := "resource \"aws_vpc\" \"main\" {}\nmodule \"net\" {\n  source = \"./child\"\n  cidr   = aws_vpc.main.cidr_block\n}\n"
	child := "variable \"cidr\" { type = string }\n"
	errs := runCrossValidate(t, parent, child)
	// No type mismatch should fire — we can't infer the resource attribute's type.
	for _, e := range errs {
		if strings.Contains(e.Msg, "passes") && strings.Contains(e.Msg, "but child variable expects") {
			t.Errorf("should not flag unknowable-type arg, got: %v", e)
		}
	}
}

func TestCrossValidateRemoteSourceSkipped(t *testing.T) {
	// A module with a registry source produces no Children entry, so the
	// cross-validator quietly skips it.
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
}

func TestCrossValidateChildWithNoTypeConstraintSkipsTypeCheck(t *testing.T) {
	// Child variable has no `type =`, so we don't type-check the argument.
	parent := "module \"net\" {\n  source = \"./child\"\n  cidr   = 42\n}\n"
	child := "variable \"cidr\" {}\n"
	errs := runCrossValidate(t, parent, child)
	for _, e := range errs {
		if strings.Contains(e.Msg, "but child variable expects") {
			t.Errorf("no type constraint → no type-mismatch error, got: %v", e)
		}
	}
}

// ---- scoped CrossValidateCall ----

func TestCrossValidateCallFocusesOnNamedModule(t *testing.T) {
	// Parent has two module calls; CrossValidateCall should only check the
	// named one, ignoring the other.
	root := t.TempDir()
	for _, sub := range []string{"a", "b"} {
		if err := os.MkdirAll(filepath.Join(root, sub), 0755); err != nil {
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
	// Only check module "a".
	errs := loader.CrossValidateCall(proj.Root.Module, "a", proj.Root.Children["a"].Module)
	if len(errs) == 0 {
		t.Fatal("expected error for missing need_a")
	}
	for _, e := range errs {
		if strings.Contains(e.Msg, "need_b") {
			t.Errorf("scoped check should not surface need_b issues: %v", e)
		}
	}
}

func TestCrossValidateCallUnknownModuleReturnsNil(t *testing.T) {
	root := t.TempDir()
	writeTF(t, root, "main.tf", `variable "x" {}`)
	proj, _, err := loader.LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	// The parent has no module calls. Asking about "nope" should return nil.
	errs := loader.CrossValidateCall(proj.Root.Module, "nope", proj.Root.Module)
	if errs != nil {
		t.Errorf("expected nil for unknown module call, got: %v", errs)
	}
}

func TestCrossValidateCallAgainstCandidateVersion(t *testing.T) {
	// Simulates the whatif use case: parent has module "vpc" with one set of
	// args; we test it against a DIFFERENT version of the child (loaded from
	// elsewhere) and get errors specific to that candidate.
	root := t.TempDir()
	childV1 := filepath.Join(root, "child-v1")
	childV2 := t.TempDir() // pretend this is "downloaded v2"
	if err := os.MkdirAll(childV1, 0755); err != nil {
		t.Fatal(err)
	}
	writeTF(t, root, "main.tf", `
module "vpc" {
  source = "./child-v1"
  cidr   = "10.0.0.0/16"
}
`)
	// v1: only cidr required.
	writeTF(t, childV1, "variables.tf",
		"variable \"cidr\" { type = string }\n")
	// v2: cidr is still required AND a new required `region` was added.
	writeTF(t, childV2, "variables.tf",
		"variable \"cidr\" { type = string }\nvariable \"region\" { type = string }\n")

	proj, _, err := loader.LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	// Sanity check against v1 — parent's cidr arg is enough, no errors.
	if errs := loader.CrossValidateCall(proj.Root.Module, "vpc", proj.Root.Children["vpc"].Module); len(errs) != 0 {
		t.Errorf("parent should be compatible with v1, got: %v", errs)
	}
	// Load v2 from disk and test against it.
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
}

// ---- transitive (parent → child → grandchild) ----

func TestCrossValidateTransitive(t *testing.T) {
	// Root → middle → leaf. Leaf has a required input not passed by middle.
	root := t.TempDir()
	mid := filepath.Join(root, "mid")
	leaf := filepath.Join(mid, "leaf")
	if err := os.MkdirAll(leaf, 0755); err != nil {
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
}
