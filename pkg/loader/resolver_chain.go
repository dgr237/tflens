package loader

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/cache"
	"github.com/dgr237/tflens/pkg/config"
	"github.com/dgr237/tflens/pkg/resolver"
)

// Loader bundles the configuration (offline mode) every project-
// loading entry point needs. Construct once per cmd invocation via
// New(s) and call Project / ProjectsForDiff / ForValidate as
// required — the offline flag is captured here so callers don't
// thread it through every method.
//
// The resolver chain itself is rebuilt per call because its manifest
// component is rooted at the project being loaded, which varies. The
// non-root-dependent parts (cache, credentials, registry, git) are
// cheap to construct repeatedly; reusing them across calls would
// only matter for batch workloads, which the cmd layer doesn't have.
type Loader struct {
	offline bool
}

// New returns a Loader configured from s. Reads s.Offline; the
// resolver chain is built lazily on each call so a no-load
// invocation pays nothing.
func New(s config.Settings) *Loader {
	return &Loader{offline: s.Offline}
}

// Project loads rootDir as a full project tree using the standard
// resolver chain. The path is absolutised first so relative module
// sources resolve consistently regardless of the caller's cwd.
//
// Returns the project plus any FileError warnings (chain-seed
// + per-file parse errors). A nil project always pairs with a
// non-nil error.
func (l *Loader) Project(rootDir string) (*Project, []FileError, error) {
	abs, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving root path: %w", err)
	}
	chain, seed, err := defaultResolverChain(abs, l.offline)
	if err != nil {
		return nil, seed, err
	}
	return loadProjectWith(abs, chain, seed)
}

// ProjectsForDiff loads both sides of a project diff: the working
// tree at path (the "new" side) and the same path checked out at
// baseRef (the "old" side, materialised in a temporary git worktree
// using the same chain).
//
// Returns both projects plus a cleanup func the caller MUST defer to
// remove the worktree. cleanup is non-nil even on the error paths
// (potentially as a no-op) so a deferred cleanup() is always safe.
//
// Used by every subcommand that compares two refs (diff, whatif,
// statediff).
func (l *Loader) ProjectsForDiff(path, baseRef string) (oldProj, newProj *Project, cleanup func(), err error) {
	noop := func() {}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, noop, fmt.Errorf("resolving path: %w", err)
	}
	chain, seed, err := defaultResolverChain(abs, l.offline)
	if err != nil {
		return nil, nil, noop, err
	}
	newProj, _, err = loadProjectWith(abs, chain, seed)
	if err != nil {
		return nil, nil, noop, fmt.Errorf("loading path: %w", err)
	}
	oldProj, cleanup, err = loadProjectAtRef(abs, baseRef, chain)
	if err != nil {
		return nil, nil, noop, err
	}
	return oldProj, newProj, cleanup, nil
}

// ForValidate is the loader-side dispatch the validate command uses:
// a single .tf file loads as just a module (no tree → no cross-
// module checks); a directory loads as a project with the standard
// resolver chain and runs CrossValidate over the result.
//
// Returns the root module, any cross-module validation errors, file
// errors collected during load, and a top-level error.
func (l *Loader) ForValidate(path string) (*analysis.Module, []analysis.ValidationError, []FileError, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, nil, err
	}
	if !info.IsDir() {
		mod, fileErrs, err := LoadFile(path)
		return mod, nil, fileErrs, err
	}
	proj, fileErrs, err := l.Project(path)
	if err != nil {
		return nil, nil, fileErrs, fmt.Errorf("loading project: %w", err)
	}
	return proj.Root.Module, CrossValidate(proj), fileErrs, nil
}

// defaultResolverChain composes the standard resolver chain:
// manifest → local → registry → git, with registry + git skipped
// when offline is true.
//
// Returns the composed chain plus seedErrors — FileError entries
// captured before parsing started (currently only a malformed
// .terraform/modules/modules.json). Callers should pass seedErrors
// into loadProjectWith so warnings surface alongside per-file parse
// errors.
//
// Credentials for registry access are loaded from $TFLENS_TFE_TOKENS_FILE
// (opt-in) merged with the standard Terraform CLI config, with TFE
// tokens winning ties. A malformed credentials file degrades to
// anonymous access with a warning printed to stderr.
func defaultResolverChain(absRoot string, offline bool) (resolver.Resolver, []FileError, error) {
	manifest, warn := resolver.NewManifestResolver(absRoot)
	var seed []FileError
	if warn != nil {
		seed = []FileError{{
			Path:   warn.Path,
			Errors: []ParseError{{Msg: warn.Msg}},
		}}
	}
	local := resolver.NewLocalResolver()
	if offline {
		return resolver.NewChain(manifest, local), seed, nil
	}

	c, err := cache.Default()
	if err != nil {
		return nil, seed, fmt.Errorf("locating module cache: %w", err)
	}
	tfrcCreds, err := resolver.LoadTerraformrc()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading Terraform CLI config: %v\n", err)
		tfrcCreds = resolver.StaticCredentials{}
	}
	tfeCreds, err := resolver.LoadTfeTokens()
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: loading TFE tokens (~/.tfe/tokens.yaml): %v\n", err)
		tfeCreds = resolver.StaticCredentials{}
	}
	// TFE tokens win over .terraformrc when both name the same host —
	// the .tfe/tokens.yaml file is typically org-managed and more
	// specific than a personal CLI config.
	creds := resolver.MergedCredentials{tfeCreds, tfrcCreds}
	git, err := resolver.NewGitResolver(resolver.GitConfig{Cache: c})
	if err != nil {
		return nil, seed, err
	}
	reg, err := resolver.NewRegistryResolver(resolver.RegistryConfig{
		Cache:       c,
		Credentials: creds,
		GitFetcher:  git,
	})
	if err != nil {
		return nil, seed, err
	}
	return resolver.NewChain(manifest, local, reg, git), seed, nil
}
