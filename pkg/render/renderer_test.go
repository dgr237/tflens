package render_test

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/render"
)

// TestNewSelectsImplementation: New(false, w) returns a ConsoleRenderer
// (verified by the human-readable "No cycles detected.\n" baseline);
// New(true, w) returns a JSONRenderer (verified by the parseable
// JSON envelope shape).
func TestNewSelectsImplementation(t *testing.T) {
	var b bytes.Buffer
	render.New(false, &b).Cycles(nil)
	if got := b.String(); got != "No cycles detected.\n" {
		t.Errorf("console output = %q, want %q", got, "No cycles detected.\n")
	}

	b.Reset()
	render.New(true, &b).Cycles(nil)
	var got struct {
		Cycles [][]string `json:"cycles"`
	}
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatalf("expected parseable JSON, got %q: %v", b.String(), err)
	}
	if len(got.Cycles) != 0 {
		t.Errorf("nil cycles should marshal to empty array, got %v", got.Cycles)
	}
}

// TestRendererCompositeSatisfiedByBothImpls is a compile-time check
// that ConsoleRenderer and JSONRenderer both fully satisfy the
// composite Renderer interface. Adding a new method to a domain
// interface without implementing it on both will fail this test.
func TestRendererCompositeSatisfiedByBothImpls(t *testing.T) {
	var _ render.Renderer = (*render.ConsoleRenderer)(nil)
	var _ render.Renderer = (*render.JSONRenderer)(nil)
}

// TestJSONRendererImpactNilAffectedBecomesEmptyArray pins down the
// "nil → []" guard so JSON consumers always receive a JSON array,
// never null.
func TestJSONRendererImpactNilAffectedBecomesEmptyArray(t *testing.T) {
	var b bytes.Buffer
	render.New(true, &b).Impact("variable.x", nil)
	if !strings.Contains(b.String(), `"affected": []`) {
		t.Errorf("expected `\"affected\": []`; got:\n%s", b.String())
	}
}

// TestJSONRendererCacheAlreadyEmptyMatchesCacheInfoShape: the JSON
// envelope for an "already empty" clear is the same shape as a
// regular CacheInfo with zero counts — consumers can treat both
// uniformly.
func TestJSONRendererCacheAlreadyEmptyMatchesCacheInfoShape(t *testing.T) {
	var b bytes.Buffer
	render.New(true, &b).CacheAlreadyEmpty("/tmp/cache")
	var got struct {
		Path    string `json:"path"`
		Entries int    `json:"entries"`
		Bytes   int64  `json:"bytes"`
	}
	if err := json.Unmarshal(b.Bytes(), &got); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Path != "/tmp/cache" || got.Entries != 0 || got.Bytes != 0 {
		t.Errorf("got %+v", got)
	}
}

// _ keeps os in scope in case future tests want to exercise the
// renderer against os.Stdout directly; harmless otherwise.
var _ = os.Stdout
