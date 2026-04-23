package lsp

import (
	"os"
	"path/filepath"
	"sync"

	"github.com/hashicorp/hcl/v2/hclparse"
	"github.com/hashicorp/hcl/v2/hclsyntax"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/loader"
)

// document is one editor-open file. The server owns the authoritative text
// while the editor is editing; the file on disk may be stale until save.
type document struct {
	URI       string
	Version   int
	Text      string
	Body      *hclsyntax.Body
	Source    []byte
	File      *analysis.File
	Module    *analysis.Module
	ParseErrs []loader.ParseError
}

// analyse re-parses the current document and builds an analysis Module that
// covers the entire containing directory, so cross-file entities are
// visible to hover, definition, and completion in this file.
func (d *document) analyse() {
	path := uriToPath(d.URI)
	src := []byte(d.Text)
	d.Source = src

	p := hclparse.NewParser()
	hclFile, diags := p.ParseHCL(src, path)
	d.ParseErrs = nil
	for _, diag := range diags {
		pe := loader.ParseError{Msg: diag.Summary}
		if diag.Subject != nil {
			pe.Pos.File = diag.Subject.Filename
			pe.Pos.Line = diag.Subject.Start.Line
			pe.Pos.Column = diag.Subject.Start.Column
		}
		d.ParseErrs = append(d.ParseErrs, pe)
	}
	if hclFile == nil {
		d.Body = nil
		d.File = nil
		d.Module = analysis.AnalyseFiles(nil)
		return
	}
	body, ok := hclFile.Body.(*hclsyntax.Body)
	if !ok {
		d.Body = nil
		d.File = nil
		d.Module = analysis.AnalyseFiles(nil)
		return
	}
	d.Body = body
	d.File = &analysis.File{Filename: path, Source: src, Body: body}

	// Collect sibling .tf files so the Module sees cross-file declarations.
	dir := filepath.Dir(path)
	entries, derr := os.ReadDir(dir)
	if derr != nil {
		d.Module = analysis.Analyse(d.File)
		return
	}
	thisBase := filepath.Base(path)
	files := []*analysis.File{d.File}
	sp := hclparse.NewParser()
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".tf" {
			continue
		}
		if entry.Name() == thisBase {
			continue
		}
		sibling := filepath.Join(dir, entry.Name())
		ssrc, err := os.ReadFile(sibling)
		if err != nil {
			continue
		}
		sf, sdiags := sp.ParseHCL(ssrc, sibling)
		if sdiags.HasErrors() || sf == nil {
			continue
		}
		sb, ok := sf.Body.(*hclsyntax.Body)
		if !ok {
			continue
		}
		files = append(files, &analysis.File{Filename: sibling, Source: ssrc, Body: sb})
	}
	d.Module = analysis.AnalyseFiles(files)
}

// store is the set of all open documents, keyed by URI. Safe for concurrent
// access — LSP clients may overlap requests.
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
