package lsp

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/ast"
	"github.com/dgr237/tflens/pkg/parser"
)

// document is one editor-open file. The server owns the authoritative text
// while the editor is editing; the file on disk may be stale until save.
type document struct {
	URI       string
	Version   int
	Text      string
	File      *ast.File
	Module    *analysis.Module
	ParseErrs []parser.ParseError
}

// analyse re-parses the current document and builds an analysis Module that
// covers the entire containing directory, so cross-file entities (for
// example variables declared in a sibling variables.tf) are visible to
// hover, definition, and completion in this file.
//
// Fails gracefully: if the directory cannot be read, falls back to a
// single-file analysis of this document only.
func (d *document) analyse() {
	path := uriToPath(d.URI)

	// Always parse the in-memory text; this file's AST drives hover and
	// definition's refAt / entityAt lookups.
	file, errs := parser.ParseFile([]byte(d.Text), path)
	d.File = file
	d.ParseErrs = errs

	// Collect sibling .tf files from the same directory so the Module sees
	// cross-file declarations.
	dir := filepath.Dir(path)
	entries, derr := os.ReadDir(dir)
	if derr != nil {
		d.Module = analysis.Analyse(file)
		return
	}

	thisBase := filepath.Base(path)
	files := []*ast.File{file}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".tf" {
			continue
		}
		if entry.Name() == thisBase {
			continue // already in files via the in-memory parse
		}
		sibling := filepath.Join(dir, entry.Name())
		src, err := os.ReadFile(sibling)
		if err != nil {
			continue
		}
		sf, _ := parser.ParseFile(src, sibling)
		files = append(files, sf)
	}
	d.Module = analysis.AnalyseFiles(files)
}

// store is the set of all open documents, keyed by URI. Safe for
// concurrent access — LSP clients may overlap requests.
type store struct {
	mu   sync.RWMutex
	docs map[string]*document
}

func newStore() *store {
	return &store{docs: make(map[string]*document)}
}

func (s *store) upsert(uri string, version int, text string) *document {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.docs[uri]
	if !ok {
		d = &document{URI: uri}
		s.docs[uri] = d
	}
	d.Version = version
	d.Text = text
	d.analyse()
	return d
}

func (s *store) get(uri string) (*document, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	d, ok := s.docs[uri]
	return d, ok
}

func (s *store) remove(uri string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.docs, uri)
}
