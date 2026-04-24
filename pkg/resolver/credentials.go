package resolver

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/zclconf/go-cty/cty"
)

// CredentialsSource supplies bearer tokens for registry hosts. A nil or
// empty source means unauthenticated.
type CredentialsSource interface {
	// Token returns the token for host, or "" when no credential is
	// configured for that host. host is the value of req.URL.Host (may
	// include a port).
	Token(host string) string
}

// StaticCredentials is a simple map-backed CredentialsSource, useful for
// tests and programmatic configuration.
type StaticCredentials map[string]string

func (s StaticCredentials) Token(host string) string { return s[host] }

// MergedCredentials tries each underlying source in order and returns
// the first non-empty token. nil entries are skipped, so callers can
// safely pass `MergedCredentials{a, b, nil}`.
type MergedCredentials []CredentialsSource

func (m MergedCredentials) Token(host string) string {
	for _, c := range m {
		if c == nil {
			continue
		}
		if tok := c.Token(host); tok != "" {
			return tok
		}
	}
	return ""
}

// LoadTerraformrc reads the user's Terraform CLI config file and returns
// the credentials it declares. Resolution order:
//
//  1. $TF_CLI_CONFIG_FILE if set
//  2. %APPDATA%\terraform.rc on Windows
//  3. ~/.terraformrc elsewhere
//
// A missing file yields an empty, non-nil source with no error. An
// unparseable file yields an error so the caller can decide whether to
// degrade to unauthenticated access or abort.
func LoadTerraformrc() (CredentialsSource, error) {
	path, err := terraformrcPath()
	if err != nil {
		return nil, err
	}
	return LoadTerraformrcFrom(path)
}

// LoadTerraformrcFrom is LoadTerraformrc with an explicit file path, for
// tests and callers that manage their own config discovery.
func LoadTerraformrcFrom(path string) (CredentialsSource, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return StaticCredentials{}, nil
		}
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	p := hclparse.NewParser()
	file, diags := p.ParseHCL(data, path)
	if diags.HasErrors() {
		return nil, fmt.Errorf("parsing %s: %s", path, diags.Error())
	}
	body, ok := file.Body.(*hclsyntax.Body)
	if !ok {
		return StaticCredentials{}, nil
	}
	return extractCredentials(body), nil
}

func terraformrcPath() (string, error) {
	if v := os.Getenv("TF_CLI_CONFIG_FILE"); v != "" {
		return v, nil
	}
	if runtime.GOOS == "windows" {
		if app := os.Getenv("APPDATA"); app != "" {
			return filepath.Join(app, "terraform.rc"), nil
		}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locating user home dir: %w", err)
	}
	return filepath.Join(home, ".terraformrc"), nil
}

// extractCredentials walks the top-level blocks of an HCL file and
// collects every `credentials "host" { token = "..." }` entry. Blocks of
// other kinds (provider_installation, plugin_cache_dir, etc.) are
// silently ignored.
func extractCredentials(body *hclsyntax.Body) StaticCredentials {
	creds := StaticCredentials{}
	if body == nil {
		return creds
	}
	for _, block := range body.Blocks {
		if block.Type != "credentials" || len(block.Labels) != 1 {
			continue
		}
		host := block.Labels[0]
		if block.Body == nil {
			continue
		}
		attr, ok := block.Body.Attributes["token"]
		if !ok {
			continue
		}
		v, vDiags := attr.Expr.Value(nil)
		if vDiags.HasErrors() || v.IsNull() || !v.Type().Equals(cty.String) {
			continue
		}
		tok := v.AsString()
		if tok == "" {
			continue
		}
		creds[host] = tok
	}
	return creds
}
