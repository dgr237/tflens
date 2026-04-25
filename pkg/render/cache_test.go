package render_test

import (
	"bytes"
	"strings"
	"testing"

	"github.com/dgr237/tflens/pkg/render"
)

func TestHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "0 B"},
		{1023, "1023 B"},
		{1024, "1.0 KiB"},
		{1536, "1.5 KiB"},
		{1024 * 1024, "1.0 MiB"},
		{1024 * 1024 * 1024, "1.0 GiB"},
	}
	for _, tc := range cases {
		if got := render.HumanBytes(tc.n); got != tc.want {
			t.Errorf("HumanBytes(%d) = %q, want %q", tc.n, got, tc.want)
		}
	}
}

func TestWriteCacheInfo(t *testing.T) {
	var b bytes.Buffer
	render.WriteCacheInfo(&b, "/tmp/cache", 7, 1536)
	want := "Path:    /tmp/cache\nEntries: 7\nSize:    1.5 KiB\n"
	if got := b.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestWriteCacheAlreadyEmpty(t *testing.T) {
	var b bytes.Buffer
	render.WriteCacheAlreadyEmpty(&b, "/tmp/cache")
	if got := b.String(); !strings.Contains(got, "/tmp/cache") || !strings.Contains(got, "already empty") {
		t.Errorf("unexpected: %q", got)
	}
}

func TestWriteCacheCleared(t *testing.T) {
	var b bytes.Buffer
	render.WriteCacheCleared(&b, 3, 2048, "/tmp/cache")
	want := "Cleared 3 entries (2.0 KiB) from /tmp/cache.\n"
	if got := b.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
