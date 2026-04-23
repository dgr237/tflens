package lsp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/hashicorp/hcl/v2"
	"github.com/hashicorp/hcl/v2/hclsyntax"
	"github.com/hashicorp/hcl/v2/hclwrite"

	"github.com/dgr237/tflens/pkg/analysis"
	"github.com/dgr237/tflens/pkg/token"
)

// ---- initialize ----

func (s *server) handleInitialize(id *json.RawMessage, p InitializeParams) {
	// Remember the workspace root for future project-wide analysis. Prefer
	// rootUri, fall back to rootPath if the client sent a path-style value.
	if p.RootURI != "" {
		s.rootPath = uriToPath(p.RootURI)
	} else if p.RootPath != "" {
		s.rootPath = p.RootPath
	} else if len(p.WorkspaceFolders) > 0 {
		s.rootPath = uriToPath(p.WorkspaceFolders[0].URI)
	}

	_ = s.out.sendResponse(id, InitializeResult{
		Capabilities: ServerCapabilities{
			TextDocumentSync:           1, // full sync
			HoverProvider:              true,
			DefinitionProvider:         true,
			DocumentSymbolProvider:     true,
			DocumentFormattingProvider: true,
			CompletionProvider: &CompletionOptions{
				TriggerCharacters: []string{"."},
			},
		},
		ServerInfo: &ServerInfo{Name: "tflens", Version: "0.1"},
	}, nil)
}

// ---- text sync ----

func (s *server) handleDidOpen(p DidOpenTextDocumentParams) {
	doc := s.store.upsert(p.TextDocument.URI, p.TextDocument.Version, p.TextDocument.Text)
	s.publishDiagnostics(doc)
}

func (s *server) handleDidChange(p DidChangeTextDocumentParams) {
	if len(p.ContentChanges) == 0 {
		return
	}
	// Full-sync mode: the last change event contains the full new text.
	text := p.ContentChanges[len(p.ContentChanges)-1].Text
	doc := s.store.upsert(p.TextDocument.URI, p.TextDocument.Version, text)
	s.publishDiagnostics(doc)
}

func (s *server) handleDidSave(p DidSaveTextDocumentParams) {
	// On save, re-analyse the document. If the client sent the content,
	// prefer that; otherwise the cached text stands.
	if p.Text != "" {
		if doc, ok := s.store.get(p.TextDocument.URI); ok {
			doc.Text = p.Text
			doc.analyse()
			s.publishDiagnostics(doc)
			return
		}
	}
	if doc, ok := s.store.get(p.TextDocument.URI); ok {
		s.publishDiagnostics(doc)
	}
}

func (s *server) handleDidClose(p DidCloseTextDocumentParams) {
	s.store.remove(p.TextDocument.URI)
	// Clear any diagnostics we published for this file.
	_ = s.out.sendNotification("textDocument/publishDiagnostics", PublishDiagnosticsParams{
		URI:         p.TextDocument.URI,
		Diagnostics: []Diagnostic{},
	})
}

// ---- diagnostics ----

// publishDiagnostics maps analysis errors from doc to LSP diagnostics and
// sends them to the client. Parse errors, validation errors, and type
// errors are all surfaced; cross-module errors would require project-level
// loading which v1 does not do on every keystroke.
func (s *server) publishDiagnostics(doc *document) {
	var diags []Diagnostic
	for _, e := range doc.ParseErrs {
		diags = append(diags, Diagnostic{
			Range:    toLSPRange(e.Pos, 1),
			Severity: SeverityError,
			Source:   "tflens",
			Message:  e.Msg,
		})
	}
	if doc.Module != nil {
		for _, e := range doc.Module.Validate() {
			diags = append(diags, Diagnostic{
				Range:    toLSPRange(e.Pos, 1),
				Severity: SeverityError,
				Source:   "tflens",
				Message:  e.Error(),
			})
		}
		for _, e := range doc.Module.TypeErrors() {
			diags = append(diags, Diagnostic{
				Range:    toLSPRange(e.Pos, 1),
				Severity: SeverityError,
				Source:   "tflens",
				Message:  e.Msg,
			})
		}
	}
	if diags == nil {
		diags = []Diagnostic{}
	}
	_ = s.out.sendNotification("textDocument/publishDiagnostics", PublishDiagnosticsParams{
		URI:         doc.URI,
		Diagnostics: diags,
	})
}

// ---- hover ----

func (s *server) handleHover(id *json.RawMessage, p HoverParams) {
	doc, ok := s.store.get(p.TextDocument.URI)
	if !ok || doc.Module == nil {
		_ = s.out.sendResponse(id, nil, nil)
		return
	}
	pos := fromLSPPos(p.Position)
	// First try: is the cursor on an entity declaration?
	if e := entityAt(doc.Module, pos); e != nil {
		_ = s.out.sendResponse(id, Hover{
			Contents: MarkupContent{Kind: "markdown", Value: describeEntity(*e)},
		}, nil)
		return
	}
	// Otherwise: is the cursor on a reference expression?
	if parts := refAt(doc.Body, pos); parts != nil {
		targetID := refToEntityID(parts)
		for _, e := range doc.Module.Entities() {
			if e.ID() == targetID {
				_ = s.out.sendResponse(id, Hover{
					Contents: MarkupContent{Kind: "markdown", Value: describeEntity(e)},
				}, nil)
				return
			}
		}
	}
	_ = s.out.sendResponse(id, nil, nil)
}

// describeEntity renders a short markdown summary for a hover popup.
func describeEntity(e analysis.Entity) string {
	var b strings.Builder
	fmt.Fprintf(&b, "**%s**\n\n", e.ID())
	fmt.Fprintf(&b, "Kind: %s\n", e.Kind)
	if e.DeclaredType != nil {
		fmt.Fprintf(&b, "\nType: `%s`", e.DeclaredType)
	}
	if e.HasDefault {
		fmt.Fprintf(&b, "\n\nHas default value")
	} else if e.Kind == analysis.KindVariable {
		fmt.Fprintf(&b, "\n\n*required input*")
	}
	if e.Sensitive {
		fmt.Fprintf(&b, "\n\n_sensitive_")
	}
	if e.NonNullable {
		fmt.Fprintf(&b, "\n\n_nullable = false_")
	}
	if e.HasCount {
		fmt.Fprintf(&b, "\n\nUses `count`")
	}
	if e.HasForEach {
		fmt.Fprintf(&b, "\n\nUses `for_each`")
	}
	if loc := e.Location(); loc != "" {
		fmt.Fprintf(&b, "\n\nDeclared at %s", loc)
	}
	return b.String()
}

// ---- definition ----

func (s *server) handleDefinition(id *json.RawMessage, p DefinitionParams) {
	doc, ok := s.store.get(p.TextDocument.URI)
	if !ok || doc.Module == nil || doc.Body == nil {
		_ = s.out.sendResponse(id, nil, nil)
		return
	}
	pos := fromLSPPos(p.Position)
	parts := refAt(doc.Body, pos)
	if parts == nil {
		_ = s.out.sendResponse(id, nil, nil)
		return
	}
	targetID := refToEntityID(parts)
	for _, e := range doc.Module.Entities() {
		if e.ID() != targetID {
			continue
		}
		_ = s.out.sendResponse(id, Location{
			URI:   pathToURI(e.Pos.File),
			Range: toLSPRange(e.Pos, 0),
		}, nil)
		return
	}
	_ = s.out.sendResponse(id, nil, nil)
}

// ---- documentSymbol ----

func (s *server) handleDocumentSymbol(id *json.RawMessage, p DocumentSymbolParams) {
	doc, ok := s.store.get(p.TextDocument.URI)
	if !ok || doc.Module == nil {
		_ = s.out.sendResponse(id, []DocumentSymbol{}, nil)
		return
	}
	syms := make([]DocumentSymbol, 0, len(doc.Module.Entities()))
	for _, e := range doc.Module.Entities() {
		syms = append(syms, DocumentSymbol{
			Name:           e.ID(),
			Detail:         string(e.Kind),
			Kind:           symbolKindFor(e.Kind),
			Range:          toLSPRange(e.Pos, 1),
			SelectionRange: toLSPRange(e.Pos, 1),
		})
	}
	_ = s.out.sendResponse(id, syms, nil)
}

func symbolKindFor(k analysis.EntityKind) SymbolKind {
	switch k {
	case analysis.KindVariable:
		return SymbolKindVariable
	case analysis.KindLocal, analysis.KindData:
		return SymbolKindField
	case analysis.KindResource:
		return SymbolKindClass
	case analysis.KindModule:
		return SymbolKindModule
	case analysis.KindOutput:
		return SymbolKindProperty
	}
	return SymbolKindVariable
}

// ---- completion ----

// handleCompletion scopes suggestions to the reference prefix immediately
// before the cursor. After "var." → variables only; after "local." → locals;
// after "module." → modules; after "data." → data sources (full "type.name"
// in one step); after a known resource type → instances of that type.
// Anywhere else, returns the empty list so the client doesn't surface stale
// workspace-wide guesses.
func (s *server) handleCompletion(id *json.RawMessage, p CompletionParams) {
	doc, ok := s.store.get(p.TextDocument.URI)
	if !ok || doc.Module == nil {
		_ = s.out.sendResponse(id, []CompletionItem{}, nil)
		return
	}

	prefix := completionPrefix(doc.Text, p.Position)
	items := completionItemsFor(prefix, doc.Module)
	if items == nil {
		items = []CompletionItem{}
	}
	_ = s.out.sendResponse(id, items, nil)
}

// completionPrefix returns the identifier-or-dot run ending at the cursor
// in text. For a cursor at the end of "x = var.", returns "var.". Handles
// multiple dot segments ("data.aws_ami.") and truncated ones alike.
func completionPrefix(text string, pos Position) string {
	offset := byteOffset(text, pos)
	if offset <= 0 || offset > len(text) {
		return ""
	}
	i := offset - 1
	for i >= 0 {
		ch := text[i]
		if isIdentByte(ch) || ch == '.' {
			i--
			continue
		}
		break
	}
	return text[i+1 : offset]
}

// byteOffset converts an LSP (line, character) to a byte offset in text.
// LSP characters are UTF-16 code units; we treat them as bytes, which is
// accurate for ASCII-only HCL content (the vast majority of real configs).
func byteOffset(text string, pos Position) int {
	line := 0
	col := 0
	for i := 0; i < len(text); i++ {
		if line == pos.Line && col == pos.Character {
			return i
		}
		if text[i] == '\n' {
			line++
			col = 0
			continue
		}
		col++
	}
	return len(text)
}

func isIdentByte(ch byte) bool {
	return (ch >= 'a' && ch <= 'z') ||
		(ch >= 'A' && ch <= 'Z') ||
		(ch >= '0' && ch <= '9') ||
		ch == '_' || ch == '-'
}

// completionItemsFor maps a prefix to completion items from mod.
func completionItemsFor(prefix string, mod *analysis.Module) []CompletionItem {
	// Exact-match prefixes first.
	switch prefix {
	case "var.":
		return itemsForKind(mod, analysis.KindVariable, CompletionKindVariable, variableDetail)
	case "local.":
		return itemsForKind(mod, analysis.KindLocal, CompletionKindField, nil)
	case "module.":
		return itemsForKind(mod, analysis.KindModule, CompletionKindModule, nil)
	case "data.":
		return dataSourceItems(mod)
	}
	// "data.TYPE." → list data sources of that type by name.
	if strings.HasPrefix(prefix, "data.") && strings.HasSuffix(prefix, ".") {
		typeName := strings.TrimSuffix(strings.TrimPrefix(prefix, "data."), ".")
		if typeName != "" && !strings.Contains(typeName, ".") {
			return dataSourceInstanceItems(mod, typeName)
		}
	}
	// "aws_vpc." etc. → if any resource of that type exists, suggest instances.
	if strings.HasSuffix(prefix, ".") && !strings.Contains(strings.TrimSuffix(prefix, "."), ".") {
		typeName := strings.TrimSuffix(prefix, ".")
		if items := resourceInstanceItems(mod, typeName); len(items) > 0 {
			return items
		}
	}
	// No scoped suggestion — return empty so the client doesn't pollute with
	// generic fallbacks.
	return nil
}

func itemsForKind(mod *analysis.Module, kind analysis.EntityKind, ciKind CompletionItemKind, detailFn func(analysis.Entity) string) []CompletionItem {
	var items []CompletionItem
	for _, e := range mod.Filter(kind) {
		it := CompletionItem{
			Label: e.Name,
			Kind:  ciKind,
		}
		if detailFn != nil {
			it.Detail = detailFn(e)
		}
		items = append(items, it)
	}
	return items
}

func variableDetail(e analysis.Entity) string {
	var parts []string
	if e.DeclaredType != nil {
		parts = append(parts, e.DeclaredType.String())
	}
	if !e.HasDefault {
		parts = append(parts, "required")
	}
	if e.Sensitive {
		parts = append(parts, "sensitive")
	}
	return strings.Join(parts, ", ")
}

// dataSourceItems returns one item per data source, inserting the full
// "type.name" so "data." + completion gives "data.type.name".
func dataSourceItems(mod *analysis.Module) []CompletionItem {
	var items []CompletionItem
	for _, e := range mod.Filter(analysis.KindData) {
		label := e.Type + "." + e.Name
		items = append(items, CompletionItem{
			Label:      label,
			Kind:       CompletionKindField,
			Detail:     "data source",
			InsertText: label,
		})
	}
	return items
}

// dataSourceInstanceItems returns the names of data sources of the given type.
func dataSourceInstanceItems(mod *analysis.Module, typeName string) []CompletionItem {
	var items []CompletionItem
	for _, e := range mod.Filter(analysis.KindData) {
		if e.Type != typeName {
			continue
		}
		items = append(items, CompletionItem{
			Label:  e.Name,
			Kind:   CompletionKindField,
			Detail: "data." + typeName,
		})
	}
	return items
}

// resourceInstanceItems returns the names of resources of the given type.
func resourceInstanceItems(mod *analysis.Module, typeName string) []CompletionItem {
	var items []CompletionItem
	for _, e := range mod.Filter(analysis.KindResource) {
		if e.Type != typeName {
			continue
		}
		items = append(items, CompletionItem{
			Label:  e.Name,
			Kind:   CompletionKindClass,
			Detail: typeName,
		})
	}
	return items
}

// ---- formatting ----

func (s *server) handleFormatting(id *json.RawMessage, p DocumentFormattingParams) {
	doc, ok := s.store.get(p.TextDocument.URI)
	if !ok || doc.Body == nil || len(doc.ParseErrs) > 0 {
		// Don't format a broken file — return no edits.
		_ = s.out.sendResponse(id, []TextEdit{}, nil)
		return
	}
	formatted := string(hclwrite.Format(doc.Source))
	if formatted == doc.Text {
		_ = s.out.sendResponse(id, []TextEdit{}, nil)
		return
	}
	// Replace the whole document with the formatted text. Compute the end
	// position from the current document content.
	endLine, endCol := endPosition(doc.Text)
	edit := TextEdit{
		Range: Range{
			Start: Position{Line: 0, Character: 0},
			End:   Position{Line: endLine, Character: endCol},
		},
		NewText: formatted,
	}
	_ = s.out.sendResponse(id, []TextEdit{edit}, nil)
}

// endPosition returns LSP (line, character) pointing past the last byte.
func endPosition(s string) (int, int) {
	line := 0
	col := 0
	for _, r := range s {
		if r == '\n' {
			line++
			col = 0
		} else {
			col++
		}
	}
	return line, col
}

// ---- helpers used by handlers ----

// entityAt finds an entity whose declaration position coincides with pos
// (same line; same column or within a small slack). Returns nil when none.
func entityAt(mod *analysis.Module, pos token.Position) *analysis.Entity {
	for _, e := range mod.Entities() {
		if e.Pos.Line == pos.Line && pos.Column >= e.Pos.Column && pos.Column <= e.Pos.Column+40 {
			ec := e
			return &ec
		}
	}
	return nil
}

// refAt finds the variable traversal whose source range contains pos and
// returns its flat parts (e.g. ["var","env"], ["aws_vpc","main","id"]).
// Returns nil when no traversal covers pos.
func refAt(body *hclsyntax.Body, pos token.Position) []string {
	if body == nil {
		return nil
	}
	var found []string
	visitExprs(body, func(expr hclsyntax.Expression) bool {
		for _, trav := range expr.Variables() {
			r := trav.SourceRange()
			if !rangeContainsPos(r, pos) {
				continue
			}
			found = traversalToParts(trav)
			return false
		}
		return true
	})
	return found
}

// visitExprs walks every attribute Expression in body and its nested blocks,
// invoking visit. visit returns false to stop the walk.
func visitExprs(body *hclsyntax.Body, visit func(hclsyntax.Expression) bool) {
	if body == nil {
		return
	}
	for _, attr := range body.Attributes {
		if !visit(attr.Expr) {
			return
		}
	}
	for _, b := range body.Blocks {
		visitExprs(b.Body, visit)
	}
}

// rangeContainsPos reports whether the LSP-derived 1-based pos lies within
// the source range r. Range Start is inclusive, End is exclusive.
func rangeContainsPos(r hcl.Range, pos token.Position) bool {
	if r.Start.Line > pos.Line || r.End.Line < pos.Line {
		return false
	}
	if r.Start.Line == pos.Line && r.Start.Column > pos.Column {
		return false
	}
	if r.End.Line == pos.Line && r.End.Column <= pos.Column {
		return false
	}
	return true
}

// traversalToParts flattens a traversal into its leading root + attribute
// chain. Mirrors analysis.traversalParts; kept here to avoid an export.
func traversalToParts(trav hcl.Traversal) []string {
	if len(trav) == 0 {
		return nil
	}
	var parts []string
	for i, step := range trav {
		switch s := step.(type) {
		case hcl.TraverseRoot:
			if i != 0 {
				return parts
			}
			parts = append(parts, s.Name)
		case hcl.TraverseAttr:
			parts = append(parts, s.Name)
		default:
			return parts
		}
	}
	return parts
}

// refToEntityID maps a reference's Parts to the canonical entity ID.
// Mirrors the same logic used in the analysis package.
func refToEntityID(parts []string) string {
	if len(parts) < 2 {
		return ""
	}
	switch parts[0] {
	case "var":
		return "variable." + parts[1]
	case "local":
		return "local." + parts[1]
	case "module":
		return "module." + parts[1]
	case "data":
		if len(parts) < 3 {
			return ""
		}
		return "data." + parts[1] + "." + parts[2]
	}
	// Resource-style: type.name
	return "resource." + parts[0] + "." + parts[1]
}
