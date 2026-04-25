package loader

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/cache"
	"github.com/dgr237/tflens/pkg/resolver"
)

// DefaultResolverChain composes the standard resolver chain used by
// every tflens subcommand: manifest → local → registry → git, with
// registry + git skipped when offline is true. The resolver is
// constructed once per command invocation and is safe to share across
// LoadProject* calls within that invocation.
//
// Returns the composed chain plus seedErrors — FileError entries
// captured before parsing started (currently only a malformed
// .terraform/modules/modules.json). Callers should pass seedErrors
// straight into LoadProjectWith so warnings surface alongside
// per-file parse errors.
//
// Credentials for registry access are loaded from $TFLENS_TFE_TOKENS_FILE
// (opt-in) merged with the standard Terraform CLI config, with TFE
// tokens winning ties. A malformed credentials file degrades to
// anonymous access with a warning printed to stderr — non-fatal so a
// broken personal config doesn't block the rest of the command.
func DefaultResolverChain(absRoot string, offline bool) (resolver.Resolver, []FileError, error) {
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
		// A malformed CLI config is a user-visible misconfiguration but
		// shouldn't abort the whole command — degrade to anonymous and warn.
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

// LoadProjectDefaults composes DefaultResolverChain with LoadProjectWith
// to give callers a single-call entry point for "load this project
// using the standard resolver chain". The path is absolutised before
// resolution so relative module sources resolve consistently regardless
// of the caller's cwd.
//
// Returns the loaded project plus any FileError warnings (chain-seed
// + per-file parse errors). A nil project always pairs with a non-nil
// error.
func LoadProjectDefaults(rootDir string, offline bool) (*Project, []FileError, error) {
	abs, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving root path: %w", err)
	}
	chain, seed, err := DefaultResolverChain(abs, offline)
	if err != nil {
		return nil, seed, err
	}
	return LoadProjectWith(abs, chain, seed)
}

// LoadProjectsForDiff loads both sides of a project diff: the working
// tree at path (the "new" side, via the standard resolver chain) and
// the same path checked out at baseRef (the "old" side, materialised
// in a temporary git worktree using the same chain).
//
// Returns both projects plus a cleanup func the caller MUST defer to
// remove the worktree. cleanup is non-nil even on the error paths
// (potentially as a no-op) so a deferred cleanup() is always safe.
//
// Used by every subcommand that compares two refs (diff, whatif,
// statediff). Replaces the cmd-side loadOldAndNew + loadOldProjectForRef
// + buildResolver glue.
func LoadProjectsForDiff(path, baseRef string, offline bool) (oldProj, newProj *Project, cleanup func(), err error) {
	noop := func() {}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, nil, noop, fmt.Errorf("resolving path: %w", err)
	}
	chain, seed, err := DefaultResolverChain(abs, offline)
	if err != nil {
		return nil, nil, noop, err
	}
	newProj, _, err = LoadProjectWith(abs, chain, seed)
	if err != nil {
		return nil, nil, noop, fmt.Errorf("loading path: %w", err)
	}
	oldProj, cleanup, err = LoadProjectAtRef(abs, baseRef, chain)
	if err != nil {
		return nil, nil, noop, err
	}
	return oldProj, newProj, cleanup, nil
}

// LoadForValidate is the loader-side dispatch the validate command
// uses: a single .tf file loads as just a module (no tree → no
// cross-module checks); a directory loads as a project with the
// standard resolver chain and runs CrossValidate over the result.
//
// Returns the root module, any cross-module validation errors, file
// errors collected during load, and a top-level error. The caller
// (cmd/validate) handles stderr output for the file errors and the
// exit code; this helper is purely about the load + cross-validate
// composition.
func LoadForValidate(path string, offline bool) (*analysis.Module, []analysis.ValidationError, []FileError, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, nil, nil, err
	}
	if !info.IsDir() {
		mod, fileErrs, err := LoadFile(path)
		return mod, nil, fileErrs, err
	}
	proj, fileErrs, err := LoadProjectDefaults(path, offline)
	if err != nil {
		return nil, nil, fileErrs, fmt.Errorf("loading project: %w", err)
	}
	return proj.Root.Module, CrossValidate(proj), fileErrs, nil
}
