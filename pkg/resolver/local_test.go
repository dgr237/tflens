package resolver_test

import (
	"context"
	"errors"
	"path/filepath"
	"testing"

	"github.com/dgr237/tflens/pkg/resolver"
)

func TestLocalResolverRelativeDotSlash(t *testing.T) {
	r := resolver.NewLocalResolver()
	got, err := r.Resolve(context.Background(), resolver.Ref{
		Source:    "./modules/vpc",
		ParentDir: "/parent",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := filepath.Clean("/parent/modules/vpc")
	if got.Dir != want {
		t.Errorf("Dir = %q, want %q", got.Dir, want)
	}
	if got.Kind != resolver.KindLocal {
		t.Errorf("Kind = %v, want KindLocal", got.Kind)
	}
}

func TestLocalResolverRelativeDotDotSlash(t *testing.T) {
	r := resolver.NewLocalResolver()
	got, err := r.Resolve(context.Background(), resolver.Ref{
		Source:    "../sibling",
		ParentDir: "/parent/child",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	want := filepath.Clean("/parent/sibling")
	if got.Dir != want {
		t.Errorf("Dir = %q, want %q", got.Dir, want)
	}
}

func TestLocalResolverRegistrySourceNotApplicable(t *testing.T) {
	r := resolver.NewLocalResolver()
	_, err := r.Resolve(context.Background(), resolver.Ref{
		Source: "terraform-aws-modules/vpc/aws",
	})
	if !errors.Is(err, resolver.ErrNotApplicable) {
		t.Errorf("registry source should return ErrNotApplicable, got %v", err)
	}
}

func TestLocalResolverGitSourceNotApplicable(t *testing.T) {
	r := resolver.NewLocalResolver()
	_, err := r.Resolve(context.Background(), resolver.Ref{
		Source: "git::https://github.com/foo/bar.git",
	})
	if !errors.Is(err, resolver.ErrNotApplicable) {
		t.Errorf("git source should return ErrNotApplicable, got %v", err)
	}
}

func TestLocalResolverEmptySourceNotApplicable(t *testing.T) {
	r := resolver.NewLocalResolver()
	_, err := r.Resolve(context.Background(), resolver.Ref{Source: ""})
	if !errors.Is(err, resolver.ErrNotApplicable) {
		t.Errorf("empty source should return ErrNotApplicable, got %v", err)
	}
}
