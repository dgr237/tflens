package render_test

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/config"
	"github.com/dgr237/tflens/pkg/render"
)

// settingsFor returns a minimal Settings literal for renderer tests:
// the supplied writer for both Out and Err, and the JSON flag set.
// Lets tests construct the renderer without standing up a cobra
// command.
func settingsFor(jsonMode bool, w *bytes.Buffer) config.Settings {
	return config.Settings{Out: w, Err: w, JSON: jsonMode}
}

// TestNewSelectsImplementation: New(s) returns a ConsoleRenderer when
// s.JSON=false (verified by the human-readable "No cycles detected.\n"
// baseline) and a JSONRenderer when s.JSON=true (verified by the
// parseable JSON envelope shape).
func TestNewSelectsImplementation(t *testing.T) {
	var b bytes.Buffer
	render.New(settingsFor(false, &b)).Cycles(nil)
	if got := b.String(); got != "No cycles detected.\n" {
		t.Errorf("console output = %q, want %q", got, "No cycles detected.\n")
	}

	b.Reset()
	render.New(settingsFor(true, &b)).Cycles(nil)
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
	render.New(settingsFor(true, &b)).Impact("variable.x", nil)
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
	render.New(settingsFor(true, &b)).CacheAlreadyEmpty("/tmp/cache")
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
