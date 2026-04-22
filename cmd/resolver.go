package cmd

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/dgr237/tflens/pkg/cache"
	"github.com/dgr237/tflens/pkg/loader"
	"github.com/dgr237/tflens/pkg/parser"
	"github.com/dgr237/tflens/pkg/resolver"
)

// loadProject is the CLI's one-stop loader: it absolutises the root,
// builds a resolver chain honouring --offline, loads the project, and
// prints any collected FileErrors to stderr as warnings. The returned
// project is always non-nil on a nil error.
func loadProject(cmd *cobra.Command, rootDir string) (*loader.Project, error) {
	abs, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, fmt.Errorf("resolving root path: %w", err)
	}
	r, seed, err := buildResolver(cmd, abs)
	if err != nil {
		return nil, err
	}
	project, fileErrs, err := loader.LoadProjectWith(abs, r, seed)
	if err != nil {
		return nil, err
	}
	printFileErrs(fileErrs)
	return project, nil
}

// buildResolver composes the resolver chain for rootDir. The order is
// manifest → local → (unless --offline) registry → git.
func buildResolver(cmd *cobra.Command, absRoot string) (resolver.Resolver, []loader.FileError, error) {
	manifest, warn := resolver.NewManifestResolver(absRoot)
	var seed []loader.FileError
	if warn != nil {
		seed = []loader.FileError{{
			Path:   warn.Path,
			Errors: []parser.ParseError{{Msg: warn.Msg}},
		}}
	}
	local := resolver.NewLocalResolver()

	offline, _ := cmd.Flags().GetBool("offline")
	if offline {
		return resolver.NewChain(manifest, local), seed, nil
	}

	c, err := cache.Default()
	if err != nil {
		return nil, seed, fmt.Errorf("locating module cache: %w", err)
	}
	creds, err := resolver.LoadTerraformrc()
	if err != nil {
		// A malformed CLI config is a user-visible misconfiguration,
		// but we should not abort the whole command over it — degrade
		// to anonymous access and warn.
		fmt.Fprintf(os.Stderr, "warning: loading Terraform CLI config: %v\n", err)
		creds = resolver.StaticCredentials{}
	}
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

func printFileErrs(fileErrs []loader.FileError) {
	for _, fe := range fileErrs {
		fmt.Fprintf(os.Stderr, "warning: parse errors in %s\n", fe.Path)
		for _, e := range fe.Errors {
			fmt.Fprintf(os.Stderr, "  %s\n", e)
		}
	}
}
