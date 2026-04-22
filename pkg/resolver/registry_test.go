package resolver_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/dgr237/tflens/pkg/cache"
	"github.com/dgr237/tflens/pkg/resolver"
)

// fakeRegistry builds an httptest.Server that speaks enough of the
// Terraform Registry protocol to satisfy RegistryResolver: service
// discovery, version listing, and download-URL redirection to a
// generated in-memory tarball.
type fakeRegistry struct {
	*httptest.Server
	versions     []string
	tarball      []byte
	downloadURL  string // set after Start to a URL on this same server
	callCount    map[string]*int64
	overrideMode string // "" | "git" to force a git:: download URL
	// requireToken, when non-empty, causes every request to return 401
	// unless its Authorization header is "Bearer <requireToken>".
	requireToken string
	// capturedAuth records the Authorization header seen by the tarball
	// endpoint (which may live on a different host in real deployments
	// but is same-host here).
	capturedAuth map[string]string // path -> auth header
}

func newFakeRegistry(t *testing.T, versions []string) *fakeRegistry {
	t.Helper()
	tarball := buildTinyModuleTarball(t)
	fr := &fakeRegistry{
		versions:     versions,
		tarball:      tarball,
		callCount:    map[string]*int64{"download": new(int64), "versions": new(int64)},
		capturedAuth: map[string]string{},
	}
	mux := http.NewServeMux()

	// wrap applies requireToken gating around any handler.
	wrap := func(h http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			fr.capturedAuth[r.URL.Path] = r.Header.Get("Authorization")
			if fr.requireToken != "" {
				got := r.Header.Get("Authorization")
				if got != "Bearer "+fr.requireToken {
					http.Error(w, "unauthorized", http.StatusUnauthorized)
					return
				}
			}
			h(w, r)
		}
	}

	mux.HandleFunc("/.well-known/terraform.json", wrap(func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{
			"modules.v1": "/v1/modules/",
		})
	}))

	mux.HandleFunc("/v1/modules/", wrap(func(w http.ResponseWriter, r *http.Request) {
		rest := strings.TrimPrefix(r.URL.Path, "/v1/modules/")
		switch {
		case strings.HasSuffix(rest, "/versions"):
			atomic.AddInt64(fr.callCount["versions"], 1)
			list := make([]map[string]string, 0, len(fr.versions))
			for _, v := range fr.versions {
				list = append(list, map[string]string{"version": v})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"modules": []map[string]any{{"versions": list}},
			})
		case strings.HasSuffix(rest, "/download"):
			atomic.AddInt64(fr.callCount["download"], 1)
			if fr.overrideMode == "git" {
				w.Header().Set("X-Terraform-Get", "git::https://github.com/example/repo?ref=v1.0.0")
			} else {
				w.Header().Set("X-Terraform-Get", fr.downloadURL)
			}
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	}))

	mux.HandleFunc("/archive.tar.gz", wrap(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/gzip")
		_, _ = w.Write(fr.tarball)
	}))

	// Use a TLS server so the "https" URL produced by discovery is valid —
	// otherwise the HTTPS-only check in fetchAndExtract rejects the URL.
	fr.Server = httptest.NewTLSServer(mux)
	fr.downloadURL = fr.Server.URL + "/archive.tar.gz"
	return fr
}

// buildTinyModuleTarball returns a tar.gz containing main.tf with known
// content at the archive root.
func buildTinyModuleTarball(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("# registry-fixture\n")
	hdr := &tar.Header{
		Name:     "main.tf",
		Mode:     0o644,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	}
	if err := tw.WriteHeader(hdr); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// newResolverForFake wires a RegistryResolver against fr with TLS
// verification disabled so the self-signed httptest cert is accepted.
// The fake's host is used as the default, so a bare "ns/name/prov"
// source is routed to it.
func newResolverForFake(t *testing.T, fr *fakeRegistry) (*resolver.RegistryResolver, *cache.Cache) {
	t.Helper()
	parsed, err := url.Parse(fr.URL)
	if err != nil {
		t.Fatalf("parse fake URL: %v", err)
	}
	c := cache.New(t.TempDir())
	r, err := resolver.NewRegistryResolver(resolver.RegistryConfig{
		Cache:       c,
		HTTPClient:  fr.Client(), // trusts the fake's self-signed cert
		DefaultHost: parsed.Host, // includes port
	})
	if err != nil {
		t.Fatalf("NewRegistryResolver: %v", err)
	}
	return r, c
}

func TestRegistryResolveHappyPath(t *testing.T) {
	fr := newFakeRegistry(t, []string{"1.0.0", "1.1.0", "1.2.0"})
	defer fr.Close()
	r, c := newResolverForFake(t, fr)

	got, err := r.Resolve(context.Background(), resolver.Ref{
		Source:  "ns/name/aws",
		Version: "~> 1.1",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if got.Version != "1.2.0" {
		t.Errorf("version = %q, want 1.2.0 (highest matching ~> 1.1)", got.Version)
	}
	if got.Kind != resolver.KindRegistry {
		t.Errorf("kind = %v, want KindRegistry", got.Kind)
	}

	// Resolved.Dir must contain the extracted tarball contents.
	body, err := os.ReadFile(filepath.Join(got.Dir, "main.tf"))
	if err != nil {
		t.Fatalf("reading extracted main.tf: %v", err)
	}
	if !strings.Contains(string(body), "registry-fixture") {
		t.Errorf("unexpected main.tf body: %q", body)
	}

	// Cache should be populated.
	_ = c
	if count := atomic.LoadInt64(fr.callCount["download"]); count != 1 {
		t.Errorf("download called %d times on first resolve, want 1", count)
	}
}

func TestRegistryResolveSecondCallUsesCache(t *testing.T) {
	fr := newFakeRegistry(t, []string{"1.0.0"})
	defer fr.Close()
	r, _ := newResolverForFake(t, fr)

	if _, err := r.Resolve(context.Background(), resolver.Ref{Source: "ns/name/aws", Version: "1.0.0"}); err != nil {
		t.Fatalf("first Resolve: %v", err)
	}
	if _, err := r.Resolve(context.Background(), resolver.Ref{Source: "ns/name/aws", Version: "1.0.0"}); err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	// The second resolve should still hit /versions (we don't cache that),
	// but must NOT hit /download.
	if count := atomic.LoadInt64(fr.callCount["download"]); count != 1 {
		t.Errorf("download called %d times total, want 1 (second call should be cache hit)", count)
	}
}

func TestRegistryResolveRespectsSubdirAttribute(t *testing.T) {
	// A module source with //subdir should return Dir pointing inside
	// the extracted tarball, not at its root.
	fr := newFakeRegistry(t, []string{"1.0.0"})
	// Replace tarball with one that has a nested dir.
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("variable \"x\" {}\n")
	_ = tw.WriteHeader(&tar.Header{
		Name:     "modules/child/variables.tf",
		Mode:     0o644,
		Size:     int64(len(body)),
		Typeflag: tar.TypeReg,
	})
	_, _ = tw.Write(body)
	tw.Close()
	gz.Close()
	fr.tarball = buf.Bytes()
	defer fr.Close()
	r, _ := newResolverForFake(t, fr)

	got, err := r.Resolve(context.Background(), resolver.Ref{
		Source:  "ns/name/aws//modules/child",
		Version: "1.0.0",
	})
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if !strings.HasSuffix(filepath.ToSlash(got.Dir), "/modules/child") {
		t.Errorf("Dir = %q, expected to end in /modules/child", got.Dir)
	}
	if _, err := os.Stat(filepath.Join(got.Dir, "variables.tf")); err != nil {
		t.Errorf("variables.tf not found in subdir: %v", err)
	}
}

func TestRegistryResolveNonRegistrySourceNotApplicable(t *testing.T) {
	fr := newFakeRegistry(t, []string{"1.0.0"})
	defer fr.Close()
	r, _ := newResolverForFake(t, fr)

	cases := []string{
		"./local",
		"git::https://example.com/repo",
		"github.com/foo/bar",
		"",
	}
	for _, src := range cases {
		_, err := r.Resolve(context.Background(), resolver.Ref{Source: src})
		if err == nil || !isNotApplicable(err) {
			t.Errorf("Resolve(%q) = %v, want ErrNotApplicable", src, err)
		}
	}
}

func TestRegistryResolveNoMatchingVersionIsError(t *testing.T) {
	fr := newFakeRegistry(t, []string{"1.0.0", "1.1.0"})
	defer fr.Close()
	r, _ := newResolverForFake(t, fr)

	_, err := r.Resolve(context.Background(), resolver.Ref{
		Source:  "ns/name/aws",
		Version: ">= 2.0.0",
	})
	if err == nil {
		t.Fatal("expected error for unsatisfiable constraint")
	}
	if !strings.Contains(err.Error(), "no published version") {
		t.Errorf("error = %v, expected 'no published version'", err)
	}
}

func TestRegistryResolveGitBackedReturnsClearError(t *testing.T) {
	fr := newFakeRegistry(t, []string{"1.0.0"})
	fr.overrideMode = "git"
	defer fr.Close()
	r, _ := newResolverForFake(t, fr)

	_, err := r.Resolve(context.Background(), resolver.Ref{
		Source:  "ns/name/aws",
		Version: "1.0.0",
	})
	if err == nil {
		t.Fatal("expected error for git-backed download URL")
	}
	if !strings.Contains(err.Error(), "VCS") {
		t.Errorf("error should mention VCS sources, got: %v", err)
	}
}

func TestRegistryResolveSendsBearerToHostWithCredentials(t *testing.T) {
	fr := newFakeRegistry(t, []string{"1.0.0"})
	fr.requireToken = "supersecret"
	defer fr.Close()

	parsed, err := url.Parse(fr.URL)
	if err != nil {
		t.Fatalf("parse fake URL: %v", err)
	}
	c := cache.New(t.TempDir())
	r, err := resolver.NewRegistryResolver(resolver.RegistryConfig{
		Cache:       c,
		HTTPClient:  fr.Client(),
		DefaultHost: parsed.Host,
		Credentials: resolver.StaticCredentials{parsed.Host: "supersecret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(context.Background(), resolver.Ref{Source: "ns/name/aws", Version: "1.0.0"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}
}

func TestRegistryResolveFailsWhenRequiredTokenMissing(t *testing.T) {
	fr := newFakeRegistry(t, []string{"1.0.0"})
	fr.requireToken = "required"
	defer fr.Close()
	r, _ := newResolverForFake(t, fr) // no credentials configured

	_, err := r.Resolve(context.Background(), resolver.Ref{Source: "ns/name/aws", Version: "1.0.0"})
	if err == nil || !strings.Contains(err.Error(), "401") {
		t.Errorf("expected 401 error, got %v", err)
	}
}

func TestRegistryResolveDoesNotLeakTokenToOtherHost(t *testing.T) {
	// Two servers: the "registry" (holds credentials) redirects the
	// download URL to the "cdn" (different host, no credentials). The
	// CDN should receive no Authorization header.
	cdn := newFakeRegistry(t, nil)
	defer cdn.Close()

	reg := newFakeRegistry(t, []string{"1.0.0"})
	reg.downloadURL = cdn.URL + "/archive.tar.gz"
	reg.requireToken = "secret"
	defer reg.Close()

	regHost, _ := url.Parse(reg.URL)
	cdnHost, _ := url.Parse(cdn.URL)

	// Use one HTTPS client that trusts both self-signed certs.
	// httptest.Server.Client() already does — reg.Client() accepts cdn's
	// cert too because httptest uses the same CA for all TLS servers in
	// a test binary.
	c := cache.New(t.TempDir())
	r, err := resolver.NewRegistryResolver(resolver.RegistryConfig{
		Cache:       c,
		HTTPClient:  reg.Client(),
		DefaultHost: regHost.Host,
		Credentials: resolver.StaticCredentials{regHost.Host: "secret"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Resolve(context.Background(), resolver.Ref{Source: "ns/name/aws", Version: "1.0.0"}); err != nil {
		t.Fatalf("Resolve: %v", err)
	}

	// The CDN must have received an empty Authorization header — the
	// registry token must NOT have followed the redirect.
	_ = cdnHost
	if got := cdn.capturedAuth["/archive.tar.gz"]; got != "" {
		t.Errorf("cross-host tarball fetch leaked Authorization header: %q", got)
	}
}

// isNotApplicable reports whether err is (or wraps) resolver.ErrNotApplicable.
func isNotApplicable(err error) bool {
	return errors.Is(err, resolver.ErrNotApplicable)
}

// compile-time assertion: RegistryResolver satisfies Resolver.
var _ resolver.Resolver = (*resolver.RegistryResolver)(nil)
