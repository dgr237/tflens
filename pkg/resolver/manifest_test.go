package resolver_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/dgr237/tflens/pkg/resolver"
)

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

func TestManifestResolverMissingFileNoWarning(t *testing.T) {
	root := t.TempDir()
	r, warn := resolver.NewManifestResolver(root)
	if warn != nil {
		t.Errorf("missing manifest should not produce a warning, got: %+v", warn)
	}
	_, err := r.Resolve(context.Background(), resolver.Ref{Key: "vpc"})
	if !errors.Is(err, resolver.ErrNotApplicable) {
		t.Errorf("empty manifest should return ErrNotApplicable, got %v", err)
	}
}

func TestManifestResolverMalformedJSONProducesWarning(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `{not valid json}`)
	r, warn := resolver.NewManifestResolver(root)
	if warn == nil {
		t.Fatal("malformed manifest should produce a warning")
	}
	if !filepath.IsAbs(warn.Path) {
		t.Errorf("warning path should be absolute: %q", warn.Path)
	}
	// Resolver still returns ErrNotApplicable — a broken manifest should not
	// block local-source fallback in the chain.
	_, err := r.Resolve(context.Background(), resolver.Ref{Key: "vpc"})
	if !errors.Is(err, resolver.ErrNotApplicable) {
		t.Errorf("broken manifest resolver should return ErrNotApplicable, got %v", err)
	}
}

func TestManifestResolverLookupByDottedKey(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `
{
  "Modules": [
    {"Key": "",       "Source": "",                              "Dir": "."},
    {"Key": "vpc",    "Source": "terraform-aws-modules/vpc/aws", "Dir": ".terraform/modules/vpc"},
    {"Key": "vpc.sg", "Source": "./submodules/sg",               "Dir": ".terraform/modules/vpc/submodules/sg"}
  ]
}
`)
	r, warn := resolver.NewManifestResolver(root)
	if warn != nil {
		t.Fatalf("unexpected warning: %+v", warn)
	}
	cases := []struct {
		key     string
		wantRel string
	}{
		{"vpc", ".terraform/modules/vpc"},
		{"vpc.sg", ".terraform/modules/vpc/submodules/sg"},
	}
	for _, tc := range cases {
		got, err := r.Resolve(context.Background(), resolver.Ref{Key: tc.key})
		if err != nil {
			t.Errorf("key %q: Resolve: %v", tc.key, err)
			continue
		}
		want := filepath.Clean(filepath.Join(root, tc.wantRel))
		if got.Dir != want {
			t.Errorf("key %q: Dir = %q, want %q", tc.key, got.Dir, want)
		}
		if got.Kind != resolver.KindManifest {
			t.Errorf("key %q: Kind = %v, want KindManifest", tc.key, got.Kind)
		}
	}
}

func TestManifestResolverUnknownKeyNotApplicable(t *testing.T) {
	root := t.TempDir()
	writeManifest(t, root, `
{
  "Modules": [
    {"Key": "",    "Source": "",                              "Dir": "."},
    {"Key": "vpc", "Source": "terraform-aws-modules/vpc/aws", "Dir": ".terraform/modules/vpc"}
  ]
}
`)
	r, _ := resolver.NewManifestResolver(root)
	_, err := r.Resolve(context.Background(), resolver.Ref{Key: "nonexistent"})
	if !errors.Is(err, resolver.ErrNotApplicable) {
		t.Errorf("unknown key should return ErrNotApplicable, got %v", err)
	}
}
