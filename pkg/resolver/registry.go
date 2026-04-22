package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"path/filepath"
	"strings"
	"sync"

	"github.com/dgr237/tflens/pkg/cache"
	"github.com/dgr237/tflens/pkg/constraint"
)

// DefaultRegistryHost is the public Terraform Registry.
const DefaultRegistryHost = "registry.terraform.io"

// RegistryConfig configures a RegistryResolver. A zero-value Config is not
// usable; at minimum a Cache must be provided.
type RegistryConfig struct {
	// Cache receives downloaded module tarballs. Required.
	Cache *cache.Cache
	// HTTPClient is used for all registry calls and tarball downloads.
	// When nil, http.DefaultClient is used.
	HTTPClient *http.Client
	// DefaultHost is the registry host used when a module source omits
	// one (the common "ns/name/provider" form). Defaults to
	// DefaultRegistryHost.
	DefaultHost string
}

// RegistryResolver resolves Terraform Registry module sources by speaking
// the Registry HTTP protocol: service discovery → version list → download
// URL → tarball fetch → extract into the cache.
//
// Private-registry authentication will be layered on in PR 2d; git-backed
// download URLs (the common case for public registry modules hosted on
// GitHub) are handled by the git resolver in PR 2e.
type RegistryResolver struct {
	cfg        RegistryConfig
	httpClient *http.Client

	discoveryMu sync.Mutex
	discovery   map[string]string // host -> modules.v1 base URL
}

func NewRegistryResolver(cfg RegistryConfig) (*RegistryResolver, error) {
	if cfg.Cache == nil {
		return nil, errors.New("RegistryConfig.Cache is required")
	}
	if cfg.DefaultHost == "" {
		cfg.DefaultHost = DefaultRegistryHost
	}
	hc := cfg.HTTPClient
	if hc == nil {
		hc = http.DefaultClient
	}
	return &RegistryResolver{
		cfg:        cfg,
		httpClient: hc,
		discovery:  make(map[string]string),
	}, nil
}

func (r *RegistryResolver) Resolve(ctx context.Context, ref Ref) (*Resolved, error) {
	src, ok := parseRegistrySource(ref.Source)
	if !ok {
		return nil, ErrNotApplicable
	}
	host := src.host
	if host == "" {
		host = r.cfg.DefaultHost
	}

	base, err := r.discover(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("discovering %s: %w", host, err)
	}

	versions, err := r.listVersions(ctx, base, src)
	if err != nil {
		return nil, fmt.Errorf("listing versions for %s/%s/%s: %w", src.ns, src.name, src.provider, err)
	}
	if len(versions) == 0 {
		return nil, fmt.Errorf("registry %s has no versions for %s/%s/%s", host, src.ns, src.name, src.provider)
	}
	chosen, err := selectVersion(ref.Version, versions)
	if err != nil {
		return nil, err
	}

	key := cache.Key{
		Kind:    cache.KindRegistry,
		Host:    host,
		Path:    src.ns + "/" + src.name + "/" + src.provider,
		Version: chosen.String(),
	}
	if r.cfg.Cache.Has(key) {
		return finaliseResolved(r.cfg.Cache.Path(key), src.subdir, chosen.String())
	}

	dir, err := r.cfg.Cache.Put(key, func(tmp string) error {
		downloadURL, err := r.getDownloadURL(ctx, base, src, chosen.String())
		if err != nil {
			return err
		}
		return r.fetchAndExtract(ctx, downloadURL, tmp)
	})
	if err != nil {
		return nil, err
	}
	return finaliseResolved(dir, src.subdir, chosen.String())
}

func finaliseResolved(baseDir, subdir, version string) (*Resolved, error) {
	dir := baseDir
	if subdir != "" {
		dir = filepath.Join(baseDir, filepath.FromSlash(subdir))
	}
	return &Resolved{Dir: dir, Version: version, Kind: KindRegistry}, nil
}

// discover performs Terraform service discovery against host. The returned
// URL is the "modules.v1" base, always with a trailing slash. Results are
// cached per RegistryResolver instance.
func (r *RegistryResolver) discover(ctx context.Context, host string) (string, error) {
	r.discoveryMu.Lock()
	if cached, ok := r.discovery[host]; ok {
		r.discoveryMu.Unlock()
		return cached, nil
	}
	r.discoveryMu.Unlock()

	u := "https://" + host + "/.well-known/terraform.json"
	body, err := r.getJSON(ctx, u, nil)
	if err != nil {
		return "", err
	}
	var payload struct {
		ModulesV1 string `json:"modules.v1"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return "", fmt.Errorf("parsing discovery document: %w", err)
	}
	if payload.ModulesV1 == "" {
		return "", fmt.Errorf("discovery document at %s lacks modules.v1", u)
	}
	base := payload.ModulesV1
	// Value may be an absolute URL or a host-relative path.
	if !strings.Contains(base, "://") {
		base = "https://" + host + base
	}
	if !strings.HasSuffix(base, "/") {
		base += "/"
	}

	r.discoveryMu.Lock()
	r.discovery[host] = base
	r.discoveryMu.Unlock()
	return base, nil
}

func (r *RegistryResolver) listVersions(ctx context.Context, base string, src registrySource) ([]constraint.V, error) {
	u := base + path.Join(src.ns, src.name, src.provider, "versions")
	body, err := r.getJSON(ctx, u, nil)
	if err != nil {
		return nil, err
	}
	var payload struct {
		Modules []struct {
			Versions []struct {
				Version string `json:"version"`
			} `json:"versions"`
		} `json:"modules"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("parsing versions response: %w", err)
	}
	if len(payload.Modules) == 0 {
		return nil, nil
	}
	out := make([]constraint.V, 0, len(payload.Modules[0].Versions))
	for _, v := range payload.Modules[0].Versions {
		parsed, err := constraint.ParseVersion(v.Version)
		if err != nil {
			continue // skip unparseable versions rather than failing the whole list
		}
		out = append(out, parsed)
	}
	return out, nil
}

// selectVersion picks the highest version matching the user's constraint
// string. An empty constraint means "any version".
func selectVersion(constraintStr string, versions []constraint.V) (constraint.V, error) {
	c, err := constraint.Parse(constraintStr)
	if err != nil {
		return constraint.V{}, fmt.Errorf("parsing version constraint %q: %w", constraintStr, err)
	}
	v, ok := constraint.Highest(c, versions)
	if !ok {
		return constraint.V{}, fmt.Errorf("no published version matches constraint %q", constraintStr)
	}
	return v, nil
}

// getDownloadURL asks the registry for the download URL of a specific
// version. The registry returns either a 204 with X-Terraform-Get, or a
// 200 with a JSON body containing the same; we handle both.
func (r *RegistryResolver) getDownloadURL(ctx context.Context, base string, src registrySource, version string) (string, error) {
	u := base + path.Join(src.ns, src.name, src.provider, version, "download")
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", err
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if loc := resp.Header.Get("X-Terraform-Get"); loc != "" {
		return loc, nil
	}
	if resp.StatusCode == http.StatusNoContent {
		return "", fmt.Errorf("registry %s returned 204 without X-Terraform-Get", u)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("registry %s returned %d", u, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	var payload struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal(body, &payload); err == nil && payload.URL != "" {
		return payload.URL, nil
	}
	return "", fmt.Errorf("registry %s returned no download URL", u)
}

// fetchAndExtract downloads from rawURL into destDir. rawURL may be an
// HTTPS tarball URL, optionally prefixed with a go-getter scheme such as
// "https://...?archive=tar.gz". Git-backed URLs ("git::...") return a
// clear error so callers know to wait for PR 2e.
func (r *RegistryResolver) fetchAndExtract(ctx context.Context, rawURL, destDir string) error {
	if strings.HasPrefix(rawURL, "git::") || strings.HasPrefix(rawURL, "hg::") {
		return fmt.Errorf("download URL %q uses a VCS scheme; VCS sources are not yet supported (PR 2e)", rawURL)
	}
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("parsing download URL: %w", err)
	}
	if parsed.Scheme != "https" && parsed.Scheme != "http" {
		return fmt.Errorf("unsupported download scheme %q", parsed.Scheme)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("downloading tarball: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("tarball download %s returned %d", rawURL, resp.StatusCode)
	}
	return extractTarGz(resp.Body, destDir)
}

// getJSON issues a GET, applies optional per-request header tweaks, and
// returns the body bounded to 1 MiB. The caller is responsible for
// unmarshalling.
func (r *RegistryResolver) getJSON(ctx context.Context, u string, decorate func(*http.Request)) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	if decorate != nil {
		decorate(req)
	}
	resp, err := r.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GET %s: %d", u, resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}
