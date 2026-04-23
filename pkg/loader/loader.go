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

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/resolver"
	"github.com/dgr237/tflens/pkg/token"
)

// ParseError is a single syntax error from parsing one .tf file.
type ParseError struct {
	Pos token.Position
	Msg string
}

func (e ParseError) Error() string {
	if e.Pos.File == "" && e.Pos.Line == 0 && e.Pos.Column == 0 {
		return e.Msg
	}
	return fmt.Sprintf("%s: %s", e.Pos, e.Msg)
}

// FileError holds the parse errors for a single source file.
type FileError struct {
	Path   string
	Errors []ParseError
}

func (fe FileError) Error() string {
	msgs := make([]string, len(fe.Errors))
	for i, e := range fe.Errors {
		msgs[i] = e.Error()
	}
	return fmt.Sprintf("%s: %s", fe.Path, strings.Join(msgs, "; "))
}

// LoadDir parses every .tf file in dir (non-recursively) and returns a
// merged analysis module. Parse errors are returned alongside a partial
// result so callers can decide whether to continue or abort.
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
	Dir      string
	Module   *analysis.Module
	Children map[string]*ModuleNode // keyed by module call name
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
	names := make([]string, 0, len(n.Children))
	for name := range n.Children {
		names = append(names, name)
	}
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
// child modules whose directories can be resolved.
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
			Errors: []ParseError{{Msg: warn.Msg}},
		})
	}
	chain := resolver.NewChain(manifestResolver, resolver.NewLocalResolver())
	return LoadProjectWith(absRoot, chain, allErrors)
}

// LoadProjectWith is LoadProject with an injected resolver.
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
func loadModuleNode(ctx context.Context, dir, parentKey string, depth int, r resolver.Resolver, visited map[string]*ModuleNode, allErrors *[]FileError) (*ModuleNode, error) {
	if depth > maxModuleDepth {
		return nil, fmt.Errorf("maximum module nesting depth (%d) exceeded at %s", maxModuleDepth, dir)
	}
	if node, ok := visited[dir]; ok {
		return node, nil
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
				continue
			}
			*allErrors = append(*allErrors, FileError{
				Path:   ref.Source,
				Errors: []ParseError{{Msg: err.Error()}},
			})
			continue
		}
		child, err := loadModuleNode(ctx, resolved.Dir, childKey, depth+1, r, visited, allErrors)
		if err != nil {
			*allErrors = append(*allErrors, FileError{
				Path:   resolved.Dir,
				Errors: []ParseError{{Msg: err.Error()}},
			})
			continue
		}
		node.Children[e.Name] = child
	}
	return node, nil
}

// parseDir reads and parses all .tf files in dir (non-recursively) via
// hclparse, returning analysis-ready *File values plus any diagnostics.
func parseDir(dir string) ([]*analysis.File, []FileError, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, nil, fmt.Errorf("reading directory %q: %w", dir, err)
	}

	p := hclparse.NewParser()
	var files []*analysis.File
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
		hclFile, diags := p.ParseHCL(src, path)
		if diags.HasErrors() {
			peList := make([]ParseError, 0, len(diags))
			for _, d := range diags {
				pos := token.Position{}
				if d.Subject != nil {
					pos = token.Position{
						File:   d.Subject.Filename,
						Line:   d.Subject.Start.Line,
						Column: d.Subject.Start.Column,
					}
				}
				peList = append(peList, ParseError{Pos: pos, Msg: d.Summary})
			}
			errs = append(errs, FileError{Path: path, Errors: peList})
		}
		if hclFile == nil {
			continue
		}
		body, ok := hclFile.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}
		files = append(files, &analysis.File{
			Filename: path,
			Source:   src,
			Body:     body,
		})
	}
	return files, errs, nil
}
