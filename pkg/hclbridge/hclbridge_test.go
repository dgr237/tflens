package hclbridge_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/hclbridge"
	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/parser"
)

func TestBridgeMatchesHandRolledOnSmoke(t *testing.T) {
	fixtures := []string{
		"../../testdata/smoke.tf",
		"../../testdata/project",
		"../../testdata/project/modules/network",
		"../../testdata/project/modules/compute",
	}
	for _, fx := range fixtures {
		t.Run(filepath.Base(fx), func(t *testing.T) {
			compareVariables(t, fx)
			compareGraph(t, fx)
		})
	}
}

func compareGraph(t *testing.T, path string) {
	t.Helper()
	oldMod := loadOld(t, path)
	res, err := hclbridge.LoadGraph(path)
	if err != nil {
		t.Fatalf("bridge load: %v", err)
	}

	// Compare dependency edges for every entity that both paths know about.
	for _, e := range oldMod.Entities() {
		id := e.ID()
		oldDeps := oldMod.Dependencies(id)
		newDeps := sortedKeys(res.Dependencies[id])
		if !equalStrings(oldDeps, newDeps) {
			t.Errorf("deps for %s differ\n  old: %v\n  new: %v", id, oldDeps, newDeps)
		}
	}

	// Compare validation errors by (EntityID, Ref) — positions may differ by
	// a column or two depending on whether we use the ref start or the
	// traversal start; the set of errors is what matters.
	oldValIDs := valIDs(oldMod.Validate())
	newValIDs := valIDs(res.ValErrors)
	for k := range oldValIDs {
		if !newValIDs[k] {
			t.Errorf("bridge missed validation error %v", k)
		}
	}
	for k := range newValIDs {
		if !oldValIDs[k] {
			t.Errorf("bridge reports validation error not in old path %v", k)
		}
	}
}

type valKey struct{ entity, ref string }

func valIDs(errs []analysis.ValidationError) map[valKey]bool {
	out := make(map[valKey]bool, len(errs))
	for _, e := range errs {
		if e.Ref == "" {
			continue // skip the sensitive-propagation subset; that's a different pass
		}
		out[valKey{e.EntityID, e.Ref}] = true
	}
	return out
}

func sortedKeys(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func compareVariables(t *testing.T, path string) {
	t.Helper()
	oldMod := loadOld(t, path)
	newEntities, newTypeErrs, err := hclbridge.LoadWithDetails(path)
	if err != nil {
		t.Fatalf("bridge load: %v", err)
	}

	oldVars := filterVars(oldMod.Entities())
	newVars := filterVars(newEntities)
	if len(oldVars) != len(newVars) {
		t.Fatalf("variable count: old=%d new=%d", len(oldVars), len(newVars))
	}

	for i, ov := range oldVars {
		nv := newVars[i]
		if ov.Name != nv.Name {
			t.Errorf("var[%d] name: old=%q new=%q", i, ov.Name, nv.Name)
		}
		if ov.HasDefault != nv.HasDefault {
			t.Errorf("var %s HasDefault: old=%v new=%v", ov.Name, ov.HasDefault, nv.HasDefault)
		}
		if ov.NonNullable != nv.NonNullable {
			t.Errorf("var %s NonNullable: old=%v new=%v", ov.Name, ov.NonNullable, nv.NonNullable)
		}
		if ov.Sensitive != nv.Sensitive {
			t.Errorf("var %s Sensitive: old=%v new=%v", ov.Name, ov.Sensitive, nv.Sensitive)
		}
		if ov.Validations != nv.Validations {
			t.Errorf("var %s Validations: old=%d new=%d", ov.Name, ov.Validations, nv.Validations)
		}
		if ov.Preconditions != nv.Preconditions {
			t.Errorf("var %s Preconditions: old=%d new=%d", ov.Name, ov.Preconditions, nv.Preconditions)
		}
		if ov.Postconditions != nv.Postconditions {
			t.Errorf("var %s Postconditions: old=%d new=%d", ov.Name, ov.Postconditions, nv.Postconditions)
		}
		oStr, nStr := typeStr(ov.DeclaredType), typeStr(nv.DeclaredType)
		if oStr != nStr {
			t.Errorf("var %s DeclaredType: old=%s new=%s", ov.Name, oStr, nStr)
		}
	}

	oldTypeErrs := varDefaultTypeErrs(oldMod.TypeErrors())
	// Bridge errors must be a subset of old errors. Extra errors from the
	// bridge would be regressions; missing errors are usually the bridge
	// being more correct (e.g. cty.convert accepts empty-object → map, which
	// the hand-rolled isCompatible spuriously rejects). Either way we want
	// to see the divergence in test output, not silently swallow it.
	oldByID := errIndex(oldTypeErrs)
	newByID := errIndex(newTypeErrs)
	for id := range newByID {
		if _, ok := oldByID[id]; !ok {
			t.Errorf("bridge reports type error not in old path: %s", id)
		}
	}
	for id, te := range oldByID {
		if _, ok := newByID[id]; !ok {
			t.Logf("bridge accepts default the old path rejects (likely an upstream-correctness win): %s — %s", id, te.Msg)
		}
	}
}

func errIndex(tes []analysis.TypeCheckError) map[string]analysis.TypeCheckError {
	out := make(map[string]analysis.TypeCheckError, len(tes))
	for _, te := range tes {
		out[te.EntityID] = te
	}
	return out
}

func TestBridgeFlagsUndefinedRefsInline(t *testing.T) {
	dir := t.TempDir()
	src := `
variable "env" {}
locals {
  x = var.missing
  y = local.ghost
  z = module.nope.id
}
output "o" { value = data.aws_foo.bar.id }
`
	p := filepath.Join(dir, "main.tf")
	if err := os.WriteFile(p, []byte(src), 0o644); err != nil {
		t.Fatal(err)
	}
	res, err := hclbridge.LoadGraph(p)
	if err != nil {
		t.Fatalf("bridge: %v", err)
	}
	want := map[string]bool{
		"variable.missing": false,
		"local.ghost":      false,
		"module.nope":      false,
		"data.aws_foo.bar": false,
	}
	for _, e := range res.ValErrors {
		if _, ok := want[e.Ref]; ok {
			want[e.Ref] = true
		}
	}
	for ref, hit := range want {
		if !hit {
			t.Errorf("bridge missed undefined ref %s; got %v", ref, res.ValErrors)
		}
	}
}

func filterVars(es []analysis.Entity) []analysis.Entity {
	var out []analysis.Entity
	for _, e := range es {
		if e.Kind == analysis.KindVariable {
			out = append(out, e)
		}
	}
	return out
}

func varDefaultTypeErrs(tes []analysis.TypeCheckError) []analysis.TypeCheckError {
	var out []analysis.TypeCheckError
	for _, te := range tes {
		if te.Attr == "default" {
			out = append(out, te)
		}
	}
	return out
}

func typeStr(t *analysis.TFType) string {
	if t == nil {
		return "<nil>"
	}
	return t.String()
}

func loadOld(t *testing.T, path string) *analysis.Module {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.IsDir() {
		mod, _, err := loader.LoadDir(path)
		if err != nil {
			t.Fatalf("LoadDir %s: %v", path, err)
		}
		return mod
	}
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	file, errs := parser.ParseFile(src, path)
	if len(errs) > 0 {
		t.Fatalf("parse %s: %v", path, errs)
	}
	return analysis.Analyse(file)
}
