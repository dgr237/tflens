package resolver

import (
	"fmt"
	"net/url"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// TfeTokensFileEnv is the environment variable that points at a TFE
// tokens YAML file. Loading is strictly opt-in: when this variable is
// unset, LoadTfeTokens returns empty credentials and never touches the
// filesystem. There is intentionally no default path — the format is
// not a Terraform standard, just a per-org convention, so we don't want
// to silently pick up an unrelated file that happens to live there.
const TfeTokensFileEnv = "TFE_TOKENS_FILE"

// LoadTfeTokens reads the TFE tokens file pointed at by $TFE_TOKENS_FILE
// and returns the credentials it declares. The file format is the YAML
// convention used by some Terraform Enterprise deployments to ship
// per-organisation tokens out-of-band from the standard ~/.terraformrc
// flow:
//
//	tokens:
//	  - address: tfe.example.com
//	    token: xyz...
//	  - address: https://other.tfe.example.com
//	    token: abc...
//
// `address` may be a bare host, a `host:port` pair, or a full URL — only
// the host (with port if non-default) is used for matching against the
// outgoing request's URL host. With $TFE_TOKENS_FILE unset, returns an
// empty, non-nil source with no error so callers can chain unconditionally.
func LoadTfeTokens() (CredentialsSource, error) {
	path := os.Getenv(TfeTokensFileEnv)
	if path == "" {
		return StaticCredentials{}, nil
	}
	return LoadTfeTokensFrom(path)
}

// LoadTfeTokensFrom is LoadTfeTokens with an explicit file path, for
// tests and callers that manage their own discovery.
func LoadTfeTokensFrom(path string) (CredentialsSource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return StaticCredentials{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var doc tfeTokensFile
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", path, err)
	}
	creds := StaticCredentials{}
	for _, t := range doc.Tokens {
		host := normaliseTokenAddress(t.Address)
		if host == "" || t.Token == "" {
			continue
		}
		creds[host] = t.Token
	}
	return creds, nil
}

type tfeTokensFile struct {
	Tokens []struct {
		Address string `yaml:"address"`
		Token   string `yaml:"token"`
	} `yaml:"tokens"`
}

// normaliseTokenAddress reduces an address value to the host form that
// will match req.URL.Host. Accepts bare hosts ("tfe.example.com"),
// host:port pairs, and full URLs ("https://tfe.example.com/path").
// Strips the default port for the URL's scheme (:443 for https, :80 for
// http) so an entry written as `https://tfe.example.com:443` still
// matches a request to `https://tfe.example.com/...`. Returns "" when
// the input cannot be reduced to a host.
func normaliseTokenAddress(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return ""
	}
	if strings.Contains(addr, "://") {
		u, err := url.Parse(addr)
		if err != nil || u.Host == "" {
			return ""
		}
		return stripDefaultPort(u.Scheme, u.Host)
	}
	// Bare host or host:port — strip any trailing path the user might
	// have included by accident, then any leading slashes.
	if i := strings.IndexAny(addr, "/?#"); i >= 0 {
		addr = addr[:i]
	}
	return addr
}

// stripDefaultPort returns host with its port removed when the port is
// the scheme's well-known default (:443 for https, :80 for http). Other
// ports — and hosts without a port — are returned unchanged. This lets
// us treat `tfe.example.com` and `tfe.example.com:443` as the same key
// for an https URL, which avoids 401s when only one side spells out the
// default port.
func stripDefaultPort(scheme, host string) string {
	idx := strings.LastIndex(host, ":")
	if idx < 0 {
		return host
	}
	// Bracketed IPv6 literals contain colons inside `[...]`; bail out.
	if strings.Contains(host, "]") {
		return host
	}
	port := host[idx+1:]
	switch {
	case scheme == "https" && port == "443":
		return host[:idx]
	case scheme == "http" && port == "80":
		return host[:idx]
	}
	return host
}
