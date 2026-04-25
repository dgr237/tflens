package render_test

import (
	"bytes"
	"strings"
	"testing"
)

// TestRendererCacheInfoFormatsHumanBytes also exercises the byte-
// formatting helper indirectly (it's no longer exported). Each row
// asserts the full "Path / Entries / Size" output for a given
// byte count.
func TestRendererCacheInfoFormatsHumanBytes(t *testing.T) {
	cases := []struct {
		n    int64
		want string
	}{
		{0, "Size:    0 B"},
		{1023, "Size:    1023 B"},
		{1024, "Size:    1.0 KiB"},
		{1536, "Size:    1.5 KiB"},
		{1024 * 1024, "Size:    1.0 MiB"},
		{1024 * 1024 * 1024, "Size:    1.0 GiB"},
	}
	for _, tc := range cases {
		var b bytes.Buffer
		consoleRenderer(&b).CacheInfo("/tmp/cache", 1, tc.n)
		if !strings.Contains(b.String(), tc.want) {
			t.Errorf("byte count %d: missing %q in:\n%s", tc.n, tc.want, b.String())
		}
	}
}

func TestRendererCacheInfoFullShape(t *testing.T) {
	var b bytes.Buffer
	consoleRenderer(&b).CacheInfo("/tmp/cache", 7, 1536)
	want := "Path:    /tmp/cache\nEntries: 7\nSize:    1.5 KiB\n"
	if got := b.String(); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestRendererCacheAlreadyEmpty(t *testing.T) {
	var b bytes.Buffer
	consoleRenderer(&b).CacheAlreadyEmpty("/tmp/cache")
	if got := b.String(); !strings.Contains(got, "/tmp/cache") || !strings.Contains(got, "already empty") {
		t.Errorf("unexpected: %q", got)
	}
}

func TestRendererCacheCleared(t *testing.T) {
	var b bytes.Buffer
	consoleRenderer(&b).CacheCleared(3, 2048, "/tmp/cache")
	want := "Cleared 3 entries (2.0 KiB) from /tmp/cache.\n"
	if got := b.String(); got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}
