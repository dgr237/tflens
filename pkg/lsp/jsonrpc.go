// Package lsp implements a minimal Language Server Protocol server for
// Terraform / HCL. It exposes the existing analysis / validate / format
// logic over LSP's JSON-RPC 2.0 transport.
package lsp

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
)

// message is the union of JSON-RPC 2.0 request, response, and notification
// envelopes. JSONRPC is always "2.0". A request carries both Method and ID;
// a notification has Method and no ID; a response has ID and either Result
// or Error.
type message struct {
	JSONRPC string           `json:"jsonrpc"`
	ID      *json.RawMessage `json:"id,omitempty"`
	Method  string           `json:"method,omitempty"`
	Params  json.RawMessage  `json:"params,omitempty"`
	Result  json.RawMessage  `json:"result,omitempty"`
	Error   *rpcError        `json:"error,omitempty"`
}

// rpcError mirrors the JSON-RPC 2.0 error object.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

// Error codes used by this server. The LSP spec assigns additional codes
// but we only need a handful for v1.
const (
	errParseError     = -32700
	errInvalidRequest = -32600
	errMethodNotFound = -32601
	errInvalidParams  = -32602
	errInternalError  = -32603
)

// reader reads LSP-framed messages from r. Each message is preceded by
// "Content-Length: N\r\n\r\n" headers.
type reader struct {
	r *bufio.Reader
}

func newReader(r io.Reader) *reader { return &reader{r: bufio.NewReader(r)} }

// readMessage blocks until a complete message is available, or returns
// io.EOF when the stream is closed.
func (rd *reader) readMessage() (*message, error) {
	var contentLen int
	for {
		line, err := rd.r.ReadString('\n')
		if err != nil {
			return nil, err
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			break
		}
		if strings.HasPrefix(line, "Content-Length:") {
			v := strings.TrimSpace(line[len("Content-Length:"):])
			n, err := strconv.Atoi(v)
			if err != nil {
				return nil, fmt.Errorf("bad Content-Length %q: %v", v, err)
			}
			contentLen = n
		}
		// Other headers (Content-Type) are ignored.
	}
	if contentLen == 0 {
		return nil, fmt.Errorf("missing Content-Length header")
	}
	buf := make([]byte, contentLen)
	if _, err := io.ReadFull(rd.r, buf); err != nil {
		return nil, err
	}
	var m message
	if err := json.Unmarshal(buf, &m); err != nil {
		return nil, fmt.Errorf("decode: %v", err)
	}
	return &m, nil
}

// writer writes LSP-framed messages to w. Safe for concurrent use because
// publishDiagnostics notifications can fire while a request handler is
// running.
type writer struct {
	mu sync.Mutex
	w  io.Writer
}

func newWriter(w io.Writer) *writer { return &writer{w: w} }

func (wr *writer) writeMessage(m *message) error {
	body, err := json.Marshal(m)
	if err != nil {
		return err
	}
	wr.mu.Lock()
	defer wr.mu.Unlock()
	if _, err := fmt.Fprintf(wr.w, "Content-Length: %d\r\n\r\n", len(body)); err != nil {
		return err
	}
	_, err = wr.w.Write(body)
	return err
}

// sendResponse writes a response for the given request ID. If err is
// non-nil, an error response is sent; otherwise result is marshaled.
func (wr *writer) sendResponse(id *json.RawMessage, result any, err *rpcError) error {
	m := &message{JSONRPC: "2.0", ID: id}
	if err != nil {
		m.Error = err
		return wr.writeMessage(m)
	}
	raw, e := json.Marshal(result)
	if e != nil {
		return e
	}
	m.Result = raw
	return wr.writeMessage(m)
}

// sendNotification writes a server-initiated notification (no ID, no response).
func (wr *writer) sendNotification(method string, params any) error {
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	return wr.writeMessage(&message{
		JSONRPC: "2.0",
		Method:  method,
		Params:  raw,
	})
}
