package lsp

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"sync/atomic"
)

// Serve runs the LSP server loop on stdin/stdout until EOF (or shutdown).
// Logging goes to stderr so it doesn't interfere with the JSON-RPC stream
// on stdout.
func Serve() {
	s := newServer(os.Stdin, os.Stdout)
	if err := s.run(); err != nil && !errors.Is(err, io.EOF) {
		log.Printf("lsp: server exited with error: %v", err)
		os.Exit(1)
	}
}

type server struct {
	in       *reader
	out      *writer
	store    *store
	rootPath string
	// shutdown is set by a `shutdown` request; the next `exit` notification
	// terminates the loop cleanly.
	shutdown atomic.Bool
}

func newServer(in io.Reader, out io.Writer) *server {
	// Redirect the default logger to stderr (stdout belongs to the protocol).
	log.SetOutput(os.Stderr)
	log.SetPrefix("[tflens-lsp] ")
	return &server{
		in:    newReader(in),
		out:   newWriter(out),
		store: newStore(),
	}
}

func (s *server) run() error {
	for {
		msg, err := s.in.readMessage()
		if err != nil {
			return err
		}
		s.dispatch(msg)
	}
}

// dispatch routes an incoming message to a handler. Requests (with ID)
// always produce a response; notifications do not.
func (s *server) dispatch(m *message) {
	isRequest := m.ID != nil

	switch m.Method {
	// ---- lifecycle ----
	case "initialize":
		var p InitializeParams
		if err := unmarshal(m.Params, &p, isRequest, s.out, m.ID); err != nil {
			return
		}
		s.handleInitialize(m.ID, p)
	case "initialized":
		// no-op
	case "shutdown":
		s.shutdown.Store(true)
		_ = s.out.sendResponse(m.ID, nil, nil)
	case "exit":
		if s.shutdown.Load() {
			os.Exit(0)
		}
		os.Exit(1)

	// ---- text sync ----
	case "textDocument/didOpen":
		var p DidOpenTextDocumentParams
		if err := json.Unmarshal(m.Params, &p); err == nil {
			s.handleDidOpen(p)
		}
	case "textDocument/didChange":
		var p DidChangeTextDocumentParams
		if err := json.Unmarshal(m.Params, &p); err == nil {
			s.handleDidChange(p)
		}
	case "textDocument/didSave":
		var p DidSaveTextDocumentParams
		if err := json.Unmarshal(m.Params, &p); err == nil {
			s.handleDidSave(p)
		}
	case "textDocument/didClose":
		var p DidCloseTextDocumentParams
		if err := json.Unmarshal(m.Params, &p); err == nil {
			s.handleDidClose(p)
		}

	// ---- requests ----
	case "textDocument/hover":
		var p HoverParams
		if err := unmarshal(m.Params, &p, isRequest, s.out, m.ID); err != nil {
			return
		}
		s.handleHover(m.ID, p)
	case "textDocument/definition":
		var p DefinitionParams
		if err := unmarshal(m.Params, &p, isRequest, s.out, m.ID); err != nil {
			return
		}
		s.handleDefinition(m.ID, p)
	case "textDocument/documentSymbol":
		var p DocumentSymbolParams
		if err := unmarshal(m.Params, &p, isRequest, s.out, m.ID); err != nil {
			return
		}
		s.handleDocumentSymbol(m.ID, p)
	case "textDocument/formatting":
		var p DocumentFormattingParams
		if err := unmarshal(m.Params, &p, isRequest, s.out, m.ID); err != nil {
			return
		}
		s.handleFormatting(m.ID, p)
	case "textDocument/completion":
		var p CompletionParams
		if err := unmarshal(m.Params, &p, isRequest, s.out, m.ID); err != nil {
			return
		}
		s.handleCompletion(m.ID, p)

	default:
		if isRequest {
			_ = s.out.sendResponse(m.ID, nil, &rpcError{
				Code:    errMethodNotFound,
				Message: fmt.Sprintf("method not supported: %s", m.Method),
			})
		}
	}
}

// unmarshal decodes params for a request. On error, sends an invalid-params
// response and returns the error so the handler can abort. For
// notifications, decode failures are silently ignored (there's no response
// channel).
func unmarshal(raw json.RawMessage, dst any, isRequest bool, out *writer, id *json.RawMessage) error {
	if err := json.Unmarshal(raw, dst); err != nil {
		if isRequest {
			_ = out.sendResponse(id, nil, &rpcError{
				Code:    errInvalidParams,
				Message: err.Error(),
			})
		}
		return err
	}
	return nil
}
