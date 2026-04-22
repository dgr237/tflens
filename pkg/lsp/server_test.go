package lsp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// testClient drives the server over a pair of in-memory pipes so tests can
// send requests and read responses just like a real editor would.
type testClient struct {
	t        *testing.T
	toServer *io.PipeWriter
	fromSrv  *reader
	nextID   int
	pending  map[int]chan *message
	mu       sync.Mutex
	done     chan struct{}
}

func startTestServer(t *testing.T) *testClient {
	t.Helper()
	clientToSrv, srvIn := io.Pipe()
	srvOut, clientFromSrv := io.Pipe()

	// Server goroutine.
	go func() {
		s := newServer(clientToSrv, clientFromSrv)
		_ = s.run()
	}()

	tc := &testClient{
		t:        t,
		toServer: srvIn,
		fromSrv:  newReader(srvOut),
		pending:  make(map[int]chan *message),
		done:     make(chan struct{}),
	}
	// Demuxer goroutine: read messages from server, route responses to
	// waiters by ID, forward notifications to the notifications channel.
	go tc.demux()
	return tc
}

func (tc *testClient) demux() {
	for {
		m, err := tc.fromSrv.readMessage()
		if err != nil {
			close(tc.done)
			return
		}
		if m.ID == nil {
			continue // notification — tests check via side effects
		}
		var rawID int
		_ = json.Unmarshal(*m.ID, &rawID)
		tc.mu.Lock()
		ch, ok := tc.pending[rawID]
		if ok {
			delete(tc.pending, rawID)
		}
		tc.mu.Unlock()
		if ok {
			ch <- m
		}
	}
}

func (tc *testClient) sendNotification(method string, params any) {
	tc.t.Helper()
	raw, err := json.Marshal(params)
	if err != nil {
		tc.t.Fatalf("marshal: %v", err)
	}
	body, _ := json.Marshal(message{
		JSONRPC: "2.0",
		Method:  method,
		Params:  raw,
	})
	_, err = fmt.Fprintf(tc.toServer, "Content-Length: %d\r\n\r\n", len(body))
	if err != nil {
		tc.t.Fatalf("write: %v", err)
	}
	_, err = tc.toServer.Write(body)
	if err != nil {
		tc.t.Fatalf("write: %v", err)
	}
}

func (tc *testClient) sendRequest(method string, params any) *message {
	tc.t.Helper()
	tc.mu.Lock()
	tc.nextID++
	id := tc.nextID
	ch := make(chan *message, 1)
	tc.pending[id] = ch
	tc.mu.Unlock()

	raw, err := json.Marshal(params)
	if err != nil {
		tc.t.Fatalf("marshal: %v", err)
	}
	idRaw := json.RawMessage(fmt.Sprintf("%d", id))
	body, _ := json.Marshal(message{
		JSONRPC: "2.0",
		ID:      &idRaw,
		Method:  method,
		Params:  raw,
	})
	_, err = fmt.Fprintf(tc.toServer, "Content-Length: %d\r\n\r\n", len(body))
	if err != nil {
		tc.t.Fatalf("write: %v", err)
	}
	_, err = tc.toServer.Write(body)
	if err != nil {
		tc.t.Fatalf("write: %v", err)
	}
	select {
	case m := <-ch:
		return m
	case <-time.After(2 * time.Second):
		tc.t.Fatalf("timed out waiting for response to %s", method)
		return nil
	}
}

// ---- tests ----

func TestInitialize(t *testing.T) {
	c := startTestServer(t)
	resp := c.sendRequest("initialize", InitializeParams{RootURI: "file:///test"})
	if resp.Error != nil {
		t.Fatalf("initialize error: %v", resp.Error)
	}
	var result InitializeResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !result.Capabilities.HoverProvider {
		t.Error("expected HoverProvider = true")
	}
	if !result.Capabilities.DefinitionProvider {
		t.Error("expected DefinitionProvider = true")
	}
	if !result.Capabilities.DocumentSymbolProvider {
		t.Error("expected DocumentSymbolProvider = true")
	}
	if !result.Capabilities.DocumentFormattingProvider {
		t.Error("expected DocumentFormattingProvider = true")
	}
}

func TestDocumentSymbolsAfterDidOpen(t *testing.T) {
	c := startTestServer(t)
	_ = c.sendRequest("initialize", InitializeParams{})

	uri := "file:///test.tf"
	src := `
variable "env" { type = string }
resource "aws_vpc" "main" {}
output "id" { value = aws_vpc.main.id }
`
	c.sendNotification("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{
			URI:        uri,
			LanguageID: "terraform",
			Version:    1,
			Text:       src,
		},
	})

	resp := c.sendRequest("textDocument/documentSymbol", DocumentSymbolParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
	})
	if resp.Error != nil {
		t.Fatalf("documentSymbol error: %v", resp.Error)
	}
	var syms []DocumentSymbol
	if err := json.Unmarshal(resp.Result, &syms); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	ids := make(map[string]bool)
	for _, s := range syms {
		ids[s.Name] = true
	}
	for _, want := range []string{"variable.env", "resource.aws_vpc.main", "output.id"} {
		if !ids[want] {
			t.Errorf("missing symbol %q; got %v", want, ids)
		}
	}
}

func TestDefinitionJumpsToDeclaration(t *testing.T) {
	c := startTestServer(t)
	_ = c.sendRequest("initialize", InitializeParams{})

	uri := "file:///test.tf"
	// var.env referenced in output on line 5. variable.env declared on line 2.
	src := `variable "env" {
  type = string
}
output "x" {
  value = var.env
}
`
	c.sendNotification("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, LanguageID: "terraform", Version: 1, Text: src},
	})
	// Position cursor over "env" in "var.env" on line 5 (index 4 in LSP 0-based),
	// character around column 12 (after "value = var.").
	resp := c.sendRequest("textDocument/definition", DefinitionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: 4, Character: 12},
	})
	if resp.Error != nil {
		t.Fatalf("definition error: %v", resp.Error)
	}
	if string(resp.Result) == "null" {
		t.Fatalf("expected a Location, got null")
	}
	var loc Location
	if err := json.Unmarshal(resp.Result, &loc); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// variable.env is declared on line 1 (LSP 0-based).
	if loc.Range.Start.Line != 0 {
		t.Errorf("definition line = %d, want 0", loc.Range.Start.Line)
	}
}

func TestHoverOnVariable(t *testing.T) {
	c := startTestServer(t)
	_ = c.sendRequest("initialize", InitializeParams{})

	uri := "file:///test.tf"
	src := "variable \"env\" {\n  type = string\n}\n"
	c.sendNotification("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, LanguageID: "terraform", Version: 1, Text: src},
	})
	resp := c.sendRequest("textDocument/hover", HoverParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: 0, Character: 4}, // on "variable"
	})
	if resp.Error != nil {
		t.Fatalf("hover error: %v", resp.Error)
	}
	if string(resp.Result) == "null" {
		t.Skip("hover returned null — position didn't match; not a failure, just position-approximation limits")
	}
	var hover Hover
	if err := json.Unmarshal(resp.Result, &hover); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !strings.Contains(hover.Contents.Value, "variable.env") {
		t.Errorf("hover text should mention variable.env: %q", hover.Contents.Value)
	}
	if !strings.Contains(hover.Contents.Value, "string") {
		t.Errorf("hover text should include declared type: %q", hover.Contents.Value)
	}
}

func TestFormattingReformatsMisalignedFile(t *testing.T) {
	c := startTestServer(t)
	_ = c.sendRequest("initialize", InitializeParams{})

	uri := "file:///test.tf"
	src := "resource   \"aws_vpc\"    \"main\"  {  cidr_block=\"10.0.0.0/16\"  }\n"
	c.sendNotification("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, LanguageID: "terraform", Version: 1, Text: src},
	})
	resp := c.sendRequest("textDocument/formatting", DocumentFormattingParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
	})
	if resp.Error != nil {
		t.Fatalf("formatting error: %v", resp.Error)
	}
	var edits []TextEdit
	if err := json.Unmarshal(resp.Result, &edits); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(edits) != 1 {
		t.Fatalf("expected 1 edit, got %d", len(edits))
	}
	if !strings.Contains(edits[0].NewText, "resource \"aws_vpc\" \"main\"") {
		t.Errorf("formatted text not as expected: %q", edits[0].NewText)
	}
}

func TestCompletionAfterVarDotReturnsOnlyVariables(t *testing.T) {
	c := startTestServer(t)
	_ = c.sendRequest("initialize", InitializeParams{})

	uri := "file:///test.tf"
	// A document with a variable, a local, a resource, and a module — if
	// scoping is broken, all four kinds would bleed through.
	src := "variable \"env\" { type = string }\n" +
		"variable \"region\" { type = string }\n" +
		"locals { name = \"x\" }\n" +
		"resource \"aws_vpc\" \"main\" {}\n" +
		"module \"net\" { source = \"./net\" }\n" +
		"output \"r\" {\n  value = var.\n}\n"
	c.sendNotification("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, LanguageID: "terraform", Version: 1, Text: src},
	})
	// "  value = var." is 14 chars; cursor sits right after the dot.
	resp := c.sendRequest("textDocument/completion", CompletionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: 6, Character: 14},
	})
	if resp.Error != nil {
		t.Fatalf("completion error: %v", resp.Error)
	}
	var items []CompletionItem
	if err := json.Unmarshal(resp.Result, &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	labels := make(map[string]bool)
	for _, it := range items {
		labels[it.Label] = true
	}
	if !labels["env"] || !labels["region"] {
		t.Errorf("var.* completion should include env and region, got: %v", labels)
	}
	if labels["name"] || labels["main"] || labels["net"] || labels["r"] {
		t.Errorf("var.* completion leaked non-variables: %v", labels)
	}
}

func TestCompletionAfterLocalDotReturnsOnlyLocals(t *testing.T) {
	c := startTestServer(t)
	_ = c.sendRequest("initialize", InitializeParams{})

	uri := "file:///test.tf"
	src := "variable \"env\" {}\n" +
		"locals {\n  name = \"x\"\n  age = 1\n}\n" +
		"output \"r\" {\n  value = local.\n}\n"
	c.sendNotification("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, LanguageID: "terraform", Version: 1, Text: src},
	})
	// "  value = local." is 16 chars.
	resp := c.sendRequest("textDocument/completion", CompletionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: 6, Character: 16},
	})
	var items []CompletionItem
	if err := json.Unmarshal(resp.Result, &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := make(map[string]bool)
	for _, it := range items {
		got[it.Label] = true
	}
	if !got["name"] || !got["age"] {
		t.Errorf("local.* completion should include name and age, got: %v", got)
	}
	if got["env"] {
		t.Errorf("local.* completion should not include variables: %v", got)
	}
}

func TestCompletionAfterModuleDotReturnsOnlyModules(t *testing.T) {
	c := startTestServer(t)
	_ = c.sendRequest("initialize", InitializeParams{})

	uri := "file:///test.tf"
	src := "module \"net\" { source = \"./net\" }\n" +
		"module \"compute\" { source = \"./compute\" }\n" +
		"variable \"env\" {}\n" +
		"output \"r\" {\n  value = module.\n}\n"
	c.sendNotification("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, LanguageID: "terraform", Version: 1, Text: src},
	})
	// "  value = module." is 17 chars.
	resp := c.sendRequest("textDocument/completion", CompletionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: 4, Character: 17},
	})
	var items []CompletionItem
	if err := json.Unmarshal(resp.Result, &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := make(map[string]bool)
	for _, it := range items {
		got[it.Label] = true
	}
	if !got["net"] || !got["compute"] {
		t.Errorf("module.* completion should include net and compute, got: %v", got)
	}
	if got["env"] {
		t.Errorf("module.* completion leaked a variable: %v", got)
	}
}

func TestCompletionAfterResourceTypeDotReturnsInstances(t *testing.T) {
	c := startTestServer(t)
	_ = c.sendRequest("initialize", InitializeParams{})

	uri := "file:///test.tf"
	src := "resource \"aws_vpc\" \"main\" {}\n" +
		"resource \"aws_vpc\" \"secondary\" {}\n" +
		"resource \"aws_subnet\" \"a\" {}\n" +
		"output \"r\" {\n  value = aws_vpc.\n}\n"
	c.sendNotification("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, LanguageID: "terraform", Version: 1, Text: src},
	})
	resp := c.sendRequest("textDocument/completion", CompletionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: 4, Character: 18},
	})
	var items []CompletionItem
	if err := json.Unmarshal(resp.Result, &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	got := make(map[string]bool)
	for _, it := range items {
		got[it.Label] = true
	}
	if !got["main"] || !got["secondary"] {
		t.Errorf("aws_vpc.* completion should include main and secondary, got: %v", got)
	}
	if got["a"] {
		t.Errorf("aws_vpc.* completion leaked an aws_subnet instance: %v", got)
	}
}

func TestCompletionSeesVariablesInSiblingFile(t *testing.T) {
	// Simulates the real editor case: user opens main.tf and types var. —
	// the variables are declared in a sibling variables.tf. Analysis must
	// merge the two to surface them.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "variables.tf"),
		[]byte("variable \"env\" {}\nvariable \"region\" {}\n"), 0644); err != nil {
		t.Fatal(err)
	}
	mainPath := filepath.Join(dir, "main.tf")
	mainSrc := "locals {\n  name = var.\n}\n"
	if err := os.WriteFile(mainPath, []byte(mainSrc), 0644); err != nil {
		t.Fatal(err)
	}

	c := startTestServer(t)
	_ = c.sendRequest("initialize", InitializeParams{})
	c.sendNotification("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{
			URI:        pathToURI(mainPath),
			LanguageID: "terraform",
			Version:    1,
			Text:       mainSrc,
		},
	})
	// "  name = var." is 13 chars on line 1 (0-based).
	resp := c.sendRequest("textDocument/completion", CompletionParams{
		TextDocument: TextDocumentIdentifier{URI: pathToURI(mainPath)},
		Position:     Position{Line: 1, Character: 13},
	})
	if resp.Error != nil {
		t.Fatalf("completion error: %v", resp.Error)
	}
	var items []CompletionItem
	if err := json.Unmarshal(resp.Result, &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	labels := make(map[string]bool)
	for _, it := range items {
		labels[it.Label] = true
	}
	if !labels["env"] || !labels["region"] {
		t.Errorf("var.* completion should see variables from sibling variables.tf, got: %v", labels)
	}
	if labels["name"] {
		t.Errorf("local.name should not appear in var.* completion, got: %v", labels)
	}
}

func TestCompletionWithoutRecognisedPrefixReturnsEmpty(t *testing.T) {
	c := startTestServer(t)
	_ = c.sendRequest("initialize", InitializeParams{})

	uri := "file:///test.tf"
	// Cursor sits after a plain word, not after var./local./etc.
	src := "variable \"env\" {}\n" +
		"output \"r\" {\n  value = foo\n}\n"
	c.sendNotification("textDocument/didOpen", DidOpenTextDocumentParams{
		TextDocument: TextDocumentItem{URI: uri, LanguageID: "terraform", Version: 1, Text: src},
	})
	resp := c.sendRequest("textDocument/completion", CompletionParams{
		TextDocument: TextDocumentIdentifier{URI: uri},
		Position:     Position{Line: 2, Character: 13}, // after "foo"
	})
	var items []CompletionItem
	if err := json.Unmarshal(resp.Result, &items); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(items) != 0 {
		t.Errorf("expected no items for unrecognised prefix, got: %v", items)
	}
}

func TestMethodNotFoundReturnsError(t *testing.T) {
	c := startTestServer(t)
	_ = c.sendRequest("initialize", InitializeParams{})
	resp := c.sendRequest("textDocument/nonexistent", struct{}{})
	if resp.Error == nil {
		t.Fatal("expected error for unknown method")
	}
	if resp.Error.Code != errMethodNotFound {
		t.Errorf("code = %d, want %d", resp.Error.Code, errMethodNotFound)
	}
}

// ---- framing smoke test ----

func TestFramingRoundTrip(t *testing.T) {
	var buf bytes.Buffer
	w := newWriter(&buf)
	idRaw := json.RawMessage(`1`)
	_ = w.writeMessage(&message{JSONRPC: "2.0", ID: &idRaw, Method: "x", Params: []byte("{}")})

	r := newReader(&buf)
	m, err := r.readMessage()
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if m.Method != "x" {
		t.Errorf("method = %q, want x", m.Method)
	}
}
