package resolver

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/dgr237/tflens/pkg/ast"
	"github.com/dgr237/tflens/pkg/parser"
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
	file, parseErrs := parser.ParseFile(data, path)
	if len(parseErrs) > 0 {
		// Return the first error — the CLI config should be syntactically
		// valid; any parse error is a user-visible misconfiguration.
		return nil, fmt.Errorf("parsing %s: %s", path, parseErrs[0].Error())
	}
	return extractCredentials(file), nil
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
func extractCredentials(file *ast.File) StaticCredentials {
	creds := StaticCredentials{}
	if file == nil || file.Body == nil {
		return creds
	}
	for _, node := range file.Body.Nodes {
		block, ok := node.(*ast.Block)
		if !ok || block.Type != "credentials" || len(block.Labels) != 1 {
			continue
		}
		host := block.Labels[0]
		if block.Body == nil {
			continue
		}
		for _, inner := range block.Body.Nodes {
			attr, ok := inner.(*ast.Attribute)
			if !ok || attr.Name != "token" {
				continue
			}
			tok, ok := literalString(attr.Value)
			if !ok || tok == "" {
				continue
			}
			creds[host] = tok
		}
	}
	return creds
}

// literalString extracts a bare string value from an expression, handling
// both LiteralExpr and single-literal TemplateExpr (the form produced for
// any quoted string). Returns ("", false) for anything more complex.
func literalString(e ast.Expr) (string, bool) {
	switch v := e.(type) {
	case *ast.LiteralExpr:
		s, ok := v.Value.(string)
		return s, ok
	case *ast.TemplateExpr:
		if len(v.Parts) != 1 || !v.Parts[0].IsLiteral {
			return "", false
		}
		return v.Parts[0].Literal, true
	}
	return "", false
}
