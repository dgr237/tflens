package resolver_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/cache"
	"github.com/dgr237/tflens/pkg/resolver"
)

// urlHost extracts the host (with port) from a full URL.
func urlHost(t *testing.T, raw string) string {
	t.Helper()
	u, err := url.Parse(raw)
	if err != nil {
		t.Fatalf("parse %q: %v", raw, err)
	}
	return u.Host
}

// makeGitRepo builds a local bare git repo that the resolver can clone
// via a file:// URL. The repo contains main.tf and an optional
// modules/child/variables.tf, committed under tag v1.0.0.
func makeGitRepo(t *testing.T, withSubmodule bool) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not on PATH")
	}
	tmp := t.TempDir()
	src := filepath.Join(tmp, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(src, "main.tf"), []byte("# git-fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if withSubmodule {
		sub := filepath.Join(src, "modules", "child")
		if err := os.MkdirAll(sub, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sub, "variables.tf"), []byte("variable \"x\" {}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	runGit := func(dir string, args ...string) {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		// Make git happy with no global config.
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=test",
			"GIT_AUTHOR_EMAIL=test@example.com",
			"GIT_COMMITTER_NAME=test",
			"GIT_COMMITTER_EMAIL=test@example.com",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
		}
	}
	runGit(src, "init", "--quiet", "-b", "main")
	runGit(src, "add", ".")
	runGit(src, "commit", "--quiet", "-m", "init")
	runGit(src, "tag", "v1.0.0")

	bare := filepath.Join(tmp, "remote.git")
	runGit(tmp, "clone", "--quiet", "--bare", src, bare)
	return bare
}

// fileURL builds a git-compatible file:// URL for a local bare repo.
// Git understands file:///C:/path on Windows and file:///tmp/path on Unix.
func fileURL(path string) string {
	p := filepath.ToSlash(path)
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return "file://" + p
}

func newGitResolver(t *testing.T) (*resolver.GitResolver, *cache.Cache) {
	t.Helper()
	c := cache.New(t.TempDir())
	gr, err := resolver.NewGitResolver(resolver.GitConfig{Cache: c})
	if err != nil {
		t.Fatalf("NewGitResolver: %v", err)
	}
	return gr, c
}

func TestGitResolveForcedScheme(t *testing.T) {
	bare := makeGitRepo(t, false)
	gr, _ := newGitResolver(t)

	src := fmt.Sprintf("git::%s?ref=v1.0.0", fileURL(bare))
	got, err := gr.Resolve(context.Background(), resolver.Ref{Source: src})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Kind != resolver.KindGit {
		t.Errorf("Kind = %v, want KindGit", got.Kind)
	}
	if got.Version != "v1.0.0" {
		t.Errorf("Version = %q, want v1.0.0", got.Version)
	}
	body, err := os.ReadFile(filepath.Join(got.Dir, "main.tf"))
	if err != nil {
		t.Fatalf("reading main.tf: %v", err)
	}
	if !strings.Contains(string(body), "git-fixture") {
		t.Errorf("main.tf = %q", body)
	}
}

func TestGitResolveSubdir(t *testing.T) {
	bare := makeGitRepo(t, true)
	gr, _ := newGitResolver(t)

	src := fmt.Sprintf("git::%s//modules/child?ref=v1.0.0", fileURL(bare))
	got, err := gr.Resolve(context.Background(), resolver.Ref{Source: src})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(got.Dir), "/modules/child") {
		t.Errorf("Dir = %q, expected .../modules/child", got.Dir)
	}
	if _, err := os.Stat(filepath.Join(got.Dir, "variables.tf")); err != nil {
		t.Errorf("variables.tf missing in subdir: %v", err)
	}
}

func TestGitResolveSecondResolveUsesCache(t *testing.T) {
	bare := makeGitRepo(t, false)
	gr, _ := newGitResolver(t)

	src := fmt.Sprintf("git::%s?ref=v1.0.0", fileURL(bare))
	got1, err := gr.Resolve(context.Background(), resolver.Ref{Source: src})
	if err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	// Drop a marker into the populated cache dir. If the second Resolve
	// re-clones, the atomic-rename in cache.Put would replace the dir
	// and the marker would disappear.
	marker := filepath.Join(got1.Dir, "_marker")
	if err := os.WriteFile(marker, []byte("x"), 0o644); err != nil {
		t.Fatalf("writing marker: %v", err)
	}

	got2, err := gr.Resolve(context.Background(), resolver.Ref{Source: src})
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if got2.Dir != got1.Dir {
		t.Errorf("second Resolve returned a different Dir: %q vs %q", got2.Dir, got1.Dir)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Errorf("second Resolve appears to have re-cloned (marker gone): %v", err)
	}
}

func TestGitResolveVersionAttributeAsRef(t *testing.T) {
	// When a module block supplies `version = "v1.0.0"` and the source
	// URL has no ref= of its own, the version attribute fills in.
	bare := makeGitRepo(t, false)
	gr, _ := newGitResolver(t)

	src := fmt.Sprintf("git::%s", fileURL(bare))
	got, err := gr.Resolve(context.Background(), resolver.Ref{
		Source:  src,
		Version: "v1.0.0",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Version != "v1.0.0" {
		t.Errorf("Version = %q, want v1.0.0", got.Version)
	}
}

func TestGitResolveNonGitSourceNotApplicable(t *testing.T) {
	gr, _ := newGitResolver(t)
	_, err := gr.Resolve(context.Background(), resolver.Ref{Source: "./local"})
	if !errors.Is(err, resolver.ErrNotApplicable) {
		t.Errorf("err = %v, want ErrNotApplicable", err)
	}
}

func TestGitResolveUnknownRefFails(t *testing.T) {
	bare := makeGitRepo(t, false)
	gr, _ := newGitResolver(t)
	src := fmt.Sprintf("git::%s?ref=no-such-ref", fileURL(bare))
	if _, err := gr.Resolve(context.Background(), resolver.Ref{Source: src}); err == nil {
		t.Error("expected error for missing ref")
	}
}

func TestRegistryWithGitBackedDelegatesToGitResolver(t *testing.T) {
	// Registry fake returns a git:: URL; resolver must delegate to the
	// configured GitFetcher rather than error.
	bare := makeGitRepo(t, false)
	gr, _ := newGitResolver(t)

	fr := newFakeRegistry(t, []string{"1.0.0"})
	fr.overrideMode = "git"
	defer fr.Close()

	// Point the fake registry's git:: URL at our local bare repo.
	// We do this by replacing the hardcoded git:: URL in the fake's
	// handler via a helper.
	fr.gitDownloadURL = fmt.Sprintf("git::%s?ref=v1.0.0", fileURL(bare))

	regURL := urlHost(t, fr.URL)
	c := cache.New(t.TempDir())
	r, err := resolver.NewRegistryResolver(resolver.RegistryConfig{
		Cache:       c,
		HTTPClient:  fr.Client(),
		DefaultHost: regURL,
		GitFetcher:  gr,
	})
	if err != nil {
		t.Fatalf("NewRegistryResolver: %v", err)
	}
	got, err := r.Resolve(context.Background(), resolver.Ref{
		Source:  "ns/name/aws",
		Version: "1.0.0",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Kind != resolver.KindGit {
		t.Errorf("Kind = %v, want KindGit (delegated)", got.Kind)
	}
	body, err := os.ReadFile(filepath.Join(got.Dir, "main.tf"))
	if err != nil || !strings.Contains(string(body), "git-fixture") {
		t.Errorf("main.tf from git-backed registry: body=%q err=%v", body, err)
	}
}
