package resolver

import (
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// LoadTfeTokens reads ~/.tfe/tokens.yaml and returns the credentials it
// declares. The file is the convention used by some Terraform Enterprise
// deployments to ship per-organisation tokens out-of-band from the
// standard `~/.terraformrc` flow:
//
//	tokens:
//	  - address: tfe.example.com
//	    token: xyz...
//	  - address: https://other.tfe.example.com
//	    token: abc...
//
// `address` may be a bare host, a `host:port` pair, or a full URL — only
// the host (with port if non-default) is used for matching against the
// outgoing request's URL host. A missing file yields an empty,
// non-nil source with no error so callers can chain unconditionally.
func LoadTfeTokens() (CredentialsSource, error) {
	path, err := tfeTokensPath()
	if err != nil {
		return nil, err
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

// tfeTokensPath returns the default location for the TFE tokens file.
// Honours $TFE_TOKENS_FILE first so tests and unusual layouts can
// override; otherwise ~/.tfe/tokens.yaml.
func tfeTokensPath() (string, error) {
	if v := os.Getenv("TFE_TOKENS_FILE"); v != "" {
		return v, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating user home dir: %w", err)
	}
	return filepath.Join(home, ".tfe", "tokens.yaml"), nil
}

// normaliseTokenAddress reduces an address value to the host form that
// will match req.URL.Host. Accepts bare hosts ("tfe.example.com"),
// host:port pairs, and full URLs ("https://tfe.example.com/path").
// Returns "" when the input cannot be reduced to a host.
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
		return u.Host
	}
	// Bare host or host:port — strip any trailing path the user might
	// have included by accident, then any leading slashes.
	if i := strings.IndexAny(addr, "/?#"); i >= 0 {
		addr = addr[:i]
	}
	return addr
}
