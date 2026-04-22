// Package loader handles filesystem concerns: finding .tf files, parsing
// directories, and walking a project's module tree. Child-module location
// is delegated to pkg/resolver.
package loader

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/ast"
	"github.com/dgr237/tflens/pkg/parser"
	"github.com/dgr237/tflens/pkg/resolver"
)

// FileError holds the parse errors for a single source file.
type FileError struct {
	Path   string
	Errors []parser.ParseError
}

func (fe FileError) Error() string {
	msgs := make([]string, len(fe.Errors))
	for i, e := range fe.Errors {
		msgs[i] = e.Error()
	}
	return fmt.Sprintf("%s: %s", fe.Path, strings.Join(msgs, "; "))
}

// LoadDir parses every .tf file in dir (non-recursively) and returns a merged
// analysis module.  Parse errors are returned alongside a partial result so
// callers can decide whether to continue or abort.
func LoadDir(dir string) (*analysis.Module, []FileError, error) {
	files, errs, err := parseDir(dir)
	if err != nil {
		return nil, nil, err
	}
	return analysis.AnalyseFiles(files), errs, nil
}

// Project is a fully loaded Terraform project tree rooted at a single directory.
type Project struct {
	Root *ModuleNode
}

// ModuleNode is a loaded Terraform module together with its direct child modules.
type ModuleNode struct {
	Dir    string
	Module *analysis.Module
	// Children is keyed by the module call name (e.g. "vpc" for module "vpc" {}).
	Children map[string]*ModuleNode
}

// Walk calls fn for every module node in the tree (pre-order: root first).
// If fn returns false the node's children are skipped.
func (p *Project) Walk(fn func(node *ModuleNode) bool) {
	walkNode(p.Root, fn)
}

func walkNode(n *ModuleNode, fn func(*ModuleNode) bool) {
	if n == nil || !fn(n) {
		return
	}
	// Iterate in sorted order for determinism.
	names := make([]string, 0, len(n.Children))
	for name := range n.Children {
		names = append(names, name)
	}
	// simple insertion sort — module counts are tiny
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	for _, name := range names {
		walkNode(n.Children[name], fn)
	}
}

// LoadProject loads the root module at rootDir and recursively loads any
// child modules whose directories can be resolved by the default resolver
// chain (manifest first, then local paths).
//
// Parse errors are collected and returned alongside the (partial) project.
func LoadProject(rootDir string) (*Project, []FileError, error) {
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving root path: %w", err)
	}

	var allErrors []FileError
	manifestResolver, warn := resolver.NewManifestResolver(absRoot)
	if warn != nil {
		allErrors = append(allErrors, FileError{
			Path:   warn.Path,
			Errors: []parser.ParseError{{Msg: warn.Msg}},
		})
	}
	chain := resolver.NewChain(manifestResolver, resolver.NewLocalResolver())
	return LoadProjectWith(absRoot, chain, allErrors)
}

// LoadProjectWith is LoadProject with an injected resolver, for tests and
// for callers (e.g. registry-backed resolvers in later PRs) that need a
// custom chain. seedErrors is prepended to the returned FileError slice so
// warnings raised while constructing the resolver can be preserved.
func LoadProjectWith(rootDir string, r resolver.Resolver, seedErrors []FileError) (*Project, []FileError, error) {
	absRoot, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, nil, fmt.Errorf("resolving root path: %w", err)
	}
	allErrors := append([]FileError(nil), seedErrors...)
	visited := make(map[string]*ModuleNode)
	root, err := loadModuleNode(context.Background(), absRoot, "", 0, r, visited, &allErrors)
	if err != nil {
		return nil, allErrors, err
	}
	return &Project{Root: root}, allErrors, nil
}

const maxModuleDepth = 10

// loadModuleNode parses one module directory and recursively its children.
// parentKey is the dotted key path of this module in the workspace (empty
// for the root); it is extended with each child's name when forming the
// child's Ref.
func loadModuleNode(ctx context.Context, dir, parentKey string, depth int, r resolver.Resolver, visited map[string]*ModuleNode, allErrors *[]FileError) (*ModuleNode, error) {
	if depth > maxModuleDepth {
		return nil, fmt.Errorf("maximum module nesting depth (%d) exceeded at %s", maxModuleDepth, dir)
	}
	if node, ok := visited[dir]; ok {
		return node, nil // already loaded (e.g. two parents share a child)
	}

	files, errs, err := parseDir(dir)
	if err != nil {
		return nil, err
	}
	*allErrors = append(*allErrors, errs...)

	mod := analysis.AnalyseFiles(files)
	node := &ModuleNode{
		Dir:      dir,
		Module:   mod,
		Children: make(map[string]*ModuleNode),
	}
	// Register before recursing so cycles don't loop forever.
	visited[dir] = node

	for _, e := range mod.Filter(analysis.KindModule) {
		childKey := e.Name
		if parentKey != "" {
			childKey = parentKey + "." + e.Name
		}
		ref := resolver.Ref{
			Source:    mod.ModuleSource(e.Name),
			Version:   mod.ModuleVersion(e.Name),
			ParentDir: dir,
			Key:       childKey,
		}
		resolved, err := r.Resolve(ctx, ref)
		if err != nil {
			if errors.Is(err, resolver.ErrNotApplicable) {
				continue // unresolvable (remote source, no manifest entry)
			}
			*allErrors = append(*allErrors, FileError{
				Path:   ref.Source,
				Errors: []parser.ParseError{{Msg: err.Error()}},
			})
			continue
		}
		child, err := loadModuleNode(ctx, resolved.Dir, childKey, depth+1, r, visited, allErrors)
		if err != nil {
			// Non-fatal: report and continue loading other modules.
			*allErrors = append(*allErrors, FileError{
				Path:   resolved.Dir,
				Errors: []parser.ParseError{{Msg: err.Error()}},
			})
			continue
		}
		node.Children[e.Name] = child
	}
	return node, nil
}

// parseDir reads and parses all .tf files in dir (non-recursively).
func parseDir(dir string) ([]*ast.File, []FileError, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("reading directory %q: %w", dir, err)
	}

	var files []*ast.File
	var errs []FileError

	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".tf" {
			continue
		}
		path := filepath.Join(dir, entry.Name())
		src, err := os.ReadFile(path)
		if err != nil {
			return nil, nil, fmt.Errorf("reading %q: %w", path, err)
		}
		f, parseErrs := parser.ParseFile(src, path)
		if len(parseErrs) > 0 {
			errs = append(errs, FileError{Path: path, Errors: parseErrs})
		}
		files = append(files, f)
	}
	return files, errs, nil
}
