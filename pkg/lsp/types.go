package lsp

// Subset of LSP types used by this server. Field names match the spec so
// that JSON tags mirror the wire format. Only what's actually sent or
// received is defined; optional fields we don't populate are omitted.

// ---- Position / Range / Location ----

type Position struct {
	Line      int `json:"line"`      // 0-based
	Character int `json:"character"` // 0-based UTF-16 (we approximate with bytes)
}

type Range struct {
	Start Position `json:"start"`
	End   Position `json:"end"`
}

type Location struct {
	URI   string `json:"uri"`
	Range Range  `json:"range"`
}

// ---- initialize ----

type InitializeParams struct {
	ProcessID        *int              `json:"processId"`
	RootURI          string            `json:"rootUri"`
	RootPath         string            `json:"rootPath"`
	WorkspaceFolders []WorkspaceFolder `json:"workspaceFolders"`
}

type WorkspaceFolder struct {
	URI  string `json:"uri"`
	Name string `json:"name"`
}

type InitializeResult struct {
	Capabilities ServerCapabilities `json:"capabilities"`
	ServerInfo   *ServerInfo        `json:"serverInfo,omitempty"`
}

type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

type ServerCapabilities struct {
	TextDocumentSync           int                `json:"textDocumentSync"` // 1 = full sync
	HoverProvider              bool               `json:"hoverProvider,omitempty"`
	DefinitionProvider         bool               `json:"definitionProvider,omitempty"`
	ReferencesProvider         bool               `json:"referencesProvider,omitempty"`
	DocumentSymbolProvider     bool               `json:"documentSymbolProvider,omitempty"`
	DocumentFormattingProvider bool               `json:"documentFormattingProvider,omitempty"`
	CompletionProvider         *CompletionOptions `json:"completionProvider,omitempty"`
}

// CompletionOptions tells the client when to ask us for completions.
type CompletionOptions struct {
	TriggerCharacters []string `json:"triggerCharacters,omitempty"`
	ResolveProvider   bool     `json:"resolveProvider,omitempty"`
}

// ---- text document ----

type TextDocumentItem struct {
	URI        string `json:"uri"`
	LanguageID string `json:"languageId"`
	Version    int    `json:"version"`
	Text       string `json:"text"`
}

type TextDocumentIdentifier struct {
	URI string `json:"uri"`
}

type VersionedTextDocumentIdentifier struct {
	URI     string `json:"uri"`
	Version int    `json:"version"`
}

type DidOpenTextDocumentParams struct {
	TextDocument TextDocumentItem `json:"textDocument"`
}

type DidChangeTextDocumentParams struct {
	TextDocument   VersionedTextDocumentIdentifier  `json:"textDocument"`
	ContentChanges []TextDocumentContentChangeEvent `json:"contentChanges"`
}

// With full sync (capability = 1) only Text is populated.
type TextDocumentContentChangeEvent struct {
	Text string `json:"text"`
}

type DidSaveTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Text         string                 `json:"text,omitempty"`
}

type DidCloseTextDocumentParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

// ---- diagnostics ----

type DiagnosticSeverity int

const (
	SeverityError   DiagnosticSeverity = 1
	SeverityWarning DiagnosticSeverity = 2
	SeverityInfo    DiagnosticSeverity = 3
	SeverityHint    DiagnosticSeverity = 4
)

type Diagnostic struct {
	Range    Range              `json:"range"`
	Severity DiagnosticSeverity `json:"severity,omitempty"`
	Source   string             `json:"source,omitempty"`
	Message  string             `json:"message"`
}

type PublishDiagnosticsParams struct {
	URI         string       `json:"uri"`
	Diagnostics []Diagnostic `json:"diagnostics"`
}

// ---- textDocument/hover ----

type HoverParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

type Hover struct {
	Contents MarkupContent `json:"contents"`
	Range    *Range        `json:"range,omitempty"`
}

type MarkupContent struct {
	Kind  string `json:"kind"` // "plaintext" or "markdown"
	Value string `json:"value"`
}

// ---- textDocument/definition ----

type DefinitionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
}

// ---- textDocument/references ----

type ReferenceParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      struct {
		IncludeDeclaration bool `json:"includeDeclaration"`
	} `json:"context"`
}

// ---- textDocument/documentSymbol ----

type DocumentSymbolParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

type SymbolKind int

// LSP SymbolKind constants we use.
const (
	SymbolKindVariable SymbolKind = 13
	SymbolKindField    SymbolKind = 8 // locals / data sources mapped here
	SymbolKindClass    SymbolKind = 5 // resources
	SymbolKindModule   SymbolKind = 2
	SymbolKindProperty SymbolKind = 7 // outputs
)

type DocumentSymbol struct {
	Name           string           `json:"name"`
	Detail         string           `json:"detail,omitempty"`
	Kind           SymbolKind       `json:"kind"`
	Range          Range            `json:"range"`
	SelectionRange Range            `json:"selectionRange"`
	Children       []DocumentSymbol `json:"children,omitempty"`
}

// ---- textDocument/completion ----

type CompletionParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
	Position     Position               `json:"position"`
	Context      *CompletionContext     `json:"context,omitempty"`
}

type CompletionContext struct {
	TriggerKind      int    `json:"triggerKind"`
	TriggerCharacter string `json:"triggerCharacter,omitempty"`
}

type CompletionItem struct {
	Label  string             `json:"label"`
	Kind   CompletionItemKind `json:"kind,omitempty"`
	Detail string             `json:"detail,omitempty"`
	// Documentation is either a plain string or a MarkupContent; we use string.
	Documentation string `json:"documentation,omitempty"`
	// InsertText is what gets inserted if different from Label. Usually same.
	InsertText string `json:"insertText,omitempty"`
}

// CompletionItemKind values we use — a subset of the LSP enum.
type CompletionItemKind int

const (
	CompletionKindText     CompletionItemKind = 1
	CompletionKindMethod   CompletionItemKind = 2
	CompletionKindField    CompletionItemKind = 5
	CompletionKindVariable CompletionItemKind = 6
	CompletionKindClass    CompletionItemKind = 7
	CompletionKindModule   CompletionItemKind = 9
	CompletionKindProperty CompletionItemKind = 10
	CompletionKindKeyword  CompletionItemKind = 14
)

// ---- textDocument/formatting ----

type DocumentFormattingParams struct {
	TextDocument TextDocumentIdentifier `json:"textDocument"`
}

type TextEdit struct {
	Range   Range  `json:"range"`
	NewText string `json:"newText"`
}
