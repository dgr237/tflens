package loader_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"github.com/dgr237/tflens/pkg/loader"
)

// writeManifest creates .terraform/modules/modules.json inside rootDir with
// the given JSON body.
func writeManifest(t *testing.T, rootDir, body string) {
	t.Helper()
	dir := filepath.Join(rootDir, ".terraform", "modules")
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "modules.json"), []byte(body), 0644); err != nil {
		t.Fatal(err)
	}
}

// ---- resolution via manifest ----

func TestManifestResolvesRegistrySource(t *testing.T) {
	// Simulate a post-`terraform init` workspace: the parent references a
	// registry module, and a modules.json maps it to a local directory that
	// holds the downloaded source.
	root := t.TempDir()
	downloadedDir := filepath.Join(root, ".terraform", "modules", "vpc")
	if err := os.MkdirAll(downloadedDir, 0755); err != nil {
		t.Fatal(err)
	}

	writeTF(t, root, "main.tf", `
module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "5.0.0"
  cidr    = "10.0.0.0/16"
}
`)
	writeTF(t, downloadedDir, "variables.tf", `
variable "cidr" { type = string }
variable "region" {
  type    = string
  default = "us-east-1"
}
`)
	writeManifest(t, root, `
{
  "Modules": [
    {"Key": "",    "Source": "",                                "Dir": "."},
    {"Key": "vpc", "Source": "terraform-aws-modules/vpc/aws",   "Dir": ".terraform/modules/vpc"}
  ]
}
`)

	proj, _, err := loader.LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	vpc, ok := proj.Root.Children["vpc"]
	if !ok {
		t.Fatalf("expected child 'vpc' to be loaded via manifest, got children: %v", proj.Root.Children)
	}
	if vpc.Dir != downloadedDir {
		t.Errorf("child Dir = %q, want %q", vpc.Dir, downloadedDir)
	}

	// Cross-validate: parent passes `cidr` (required) but omits no required;
	// region has a default. Should produce no errors.
	if errs := loader.CrossValidate(proj); len(errs) != 0 {
		t.Errorf("expected no cross-validate errors, got: %v", errs)
	}
}

func TestManifestDetectsMissingRequiredInputForRegistryModule(t *testing.T) {
	root := t.TempDir()
	downloadedDir := filepath.Join(root, ".terraform", "modules", "vpc")
	if err := os.MkdirAll(downloadedDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeTF(t, root, "main.tf", `
module "vpc" {
  source  = "terraform-aws-modules/vpc/aws"
  version = "5.0.0"
}
`)
	writeTF(t, downloadedDir, "variables.tf",
		"variable \"cidr\" { type = string }\n")
	writeManifest(t, root, `
{
  "Modules": [
    {"Key": "",    "Source": "",                                "Dir": "."},
    {"Key": "vpc", "Source": "terraform-aws-modules/vpc/aws",   "Dir": ".terraform/modules/vpc"}
  ]
}
`)

	proj, _, err := loader.LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	errs := loader.CrossValidate(proj)
	found := false
	for _, e := range errs {
		if e.EntityID == "module.vpc" && strings.Contains(e.Msg, "cidr") && strings.Contains(e.Msg, "required input") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected required-input error against registry module, got: %v", errs)
	}
}

func TestManifestDetectsTypeMismatchForRegistryModule(t *testing.T) {
	root := t.TempDir()
	downloadedDir := filepath.Join(root, ".terraform", "modules", "vpc")
	if err := os.MkdirAll(downloadedDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeTF(t, root, "main.tf", `
module "vpc" {
  source = "terraform-aws-modules/vpc/aws"
  cidr   = 42
}
`)
	writeTF(t, downloadedDir, "variables.tf",
		"variable \"cidr\" { type = string }\n")
	writeManifest(t, root, `
{
  "Modules": [
    {"Key": "",    "Source": "",                              "Dir": "."},
    {"Key": "vpc", "Source": "terraform-aws-modules/vpc/aws", "Dir": ".terraform/modules/vpc"}
  ]
}
`)

	proj, _, err := loader.LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	errs := loader.CrossValidate(proj)
	found := false
	for _, e := range errs {
		if e.EntityID == "module.vpc" && strings.Contains(e.Msg, "number") && strings.Contains(e.Msg, "string") {
			found = true
		}
	}
	if !found {
		t.Errorf("expected type mismatch against registry module, got: %v", errs)
	}
}

// ---- transitive keys ----

func TestManifestResolvesNestedDottedKeys(t *testing.T) {
	// Root -> vpc (registry) -> sg (submodule of vpc). Manifest Key for the
	// grandchild is "vpc.sg".
	root := t.TempDir()
	vpcDir := filepath.Join(root, ".terraform", "modules", "vpc")
	sgDir := filepath.Join(vpcDir, "submodules", "sg")
	if err := os.MkdirAll(sgDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeTF(t, root, "main.tf", `
module "vpc" {
  source = "terraform-aws-modules/vpc/aws"
  cidr   = "10.0.0.0/16"
}
`)
	writeTF(t, vpcDir, "main.tf", `
variable "cidr" { type = string }
module "sg" {
  source = "./submodules/sg"
  name   = "default"
}
`)
	writeTF(t, sgDir, "variables.tf",
		"variable \"name\" { type = string }\n")
	writeManifest(t, root, `
{
  "Modules": [
    {"Key": "",       "Source": "",                              "Dir": "."},
    {"Key": "vpc",    "Source": "terraform-aws-modules/vpc/aws", "Dir": ".terraform/modules/vpc"},
    {"Key": "vpc.sg", "Source": "./submodules/sg",               "Dir": ".terraform/modules/vpc/submodules/sg"}
  ]
}
`)

	proj, _, err := loader.LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	vpc, ok := proj.Root.Children["vpc"]
	if !ok {
		t.Fatal("expected vpc child")
	}
	sg, ok := vpc.Children["sg"]
	if !ok {
		t.Fatal("expected vpc.sg grandchild")
	}
	if sg.Dir != sgDir {
		t.Errorf("sg.Dir = %q, want %q", sg.Dir, sgDir)
	}
	if errs := loader.CrossValidate(proj); len(errs) != 0 {
		t.Errorf("expected no cross errors in well-formed nested project, got: %v", errs)
	}
}

// ---- fallback behaviour ----

func TestNoManifestLocalSourcesStillWork(t *testing.T) {
	// Without a manifest, local path sources resolve as before.
	root := t.TempDir()
	childDir := filepath.Join(root, "modules", "child")
	if err := os.MkdirAll(childDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeTF(t, root, "main.tf",
		"module \"child\" { source = \"./modules/child\" }\n")
	writeTF(t, childDir, "variables.tf",
		"variable \"x\" { type = string }\n")

	proj, _, err := loader.LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if _, ok := proj.Root.Children["child"]; !ok {
		t.Error("local-path child should still be loaded without a manifest")
	}
}

func TestNoManifestRegistrySourceStillSkipped(t *testing.T) {
	// No manifest and a registry source → not loaded, but not an error.
	root := t.TempDir()
	writeTF(t, root, "main.tf", `
module "vpc" {
  source = "terraform-aws-modules/vpc/aws"
}
`)
	proj, _, err := loader.LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if len(proj.Root.Children) != 0 {
		t.Errorf("no manifest + registry source should produce no children, got: %v", proj.Root.Children)
	}
	if errs := loader.CrossValidate(proj); len(errs) != 0 {
		t.Errorf("no manifest → no cross errors on remote module, got: %v", errs)
	}
}

func TestManifestPartialCoverageFallsBackToLocal(t *testing.T) {
	// Manifest has only some entries; a local-path child not in manifest
	// should still resolve via the local-source fallback.
	root := t.TempDir()
	localChildDir := filepath.Join(root, "modules", "local")
	if err := os.MkdirAll(localChildDir, 0755); err != nil {
		t.Fatal(err)
	}
	writeTF(t, root, "main.tf", `
module "local" { source = "./modules/local" }
`)
	writeTF(t, localChildDir, "variables.tf",
		"variable \"x\" { type = string, default = \"ok\" }\n")

	// A manifest that doesn't mention "local".
	writeManifest(t, root, `
{
  "Modules": [
    {"Key": "",      "Source": "",                                "Dir": "."},
    {"Key": "other", "Source": "terraform-aws-modules/other/aws", "Dir": ".terraform/modules/other"}
  ]
}
`)

	proj, _, err := loader.LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject: %v", err)
	}
	if _, ok := proj.Root.Children["local"]; !ok {
		t.Errorf("local child should resolve via fallback when manifest omits it, got children: %v", proj.Root.Children)
	}
}

func TestMalformedManifestIsReportedNotFatal(t *testing.T) {
	// A broken manifest should surface as a FileError warning but not block
	// the rest of the project from loading.
	root := t.TempDir()
	writeTF(t, root, "main.tf", `variable "x" {}`)
	writeManifest(t, root, `{not valid json}`)

	proj, fileErrs, err := loader.LoadProject(root)
	if err != nil {
		t.Fatalf("LoadProject should not fatally error: %v", err)
	}
	if proj == nil || proj.Root == nil {
		t.Fatal("project should still load")
	}
	// The broken manifest should appear in fileErrs.
	reported := false
	for _, fe := range fileErrs {
		if strings.Contains(fe.Path, "modules.json") {
			reported = true
		}
	}
	if !reported {
		t.Errorf("malformed manifest should be reported as a parse warning, got: %v", fileErrs)
	}
}
