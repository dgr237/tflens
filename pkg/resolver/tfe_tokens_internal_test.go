package resolver

import "testing"

// TestStripDefaultPort covers the helper used by both the YAML loader
// and RegistryResolver.authorize. The two call sites use it
// symmetrically so a credential stored under bare host matches a
// request whose URL spells out :443, and a credential stored under
// host:443 matches a request to bare host.
func TestStripDefaultPort(t *testing.T) {
	cases := []struct {
		scheme, in, want string
	}{
		// Default ports stripped for matching scheme.
		{"https", "tfe.example.com:443", "tfe.example.com"},
		{"http", "insecure.example.com:80", "insecure.example.com"},
		// Non-default ports preserved.
		{"https", "tfe.example.com:8443", "tfe.example.com:8443"},
		{"http", "insecure.example.com:8080", "insecure.example.com:8080"},
		// Wrong scheme — port not stripped (e.g. https on :80 is unusual but valid).
		{"http", "tfe.example.com:443", "tfe.example.com:443"},
		{"https", "tfe.example.com:80", "tfe.example.com:80"},
		// No port — passthrough.
		{"https", "tfe.example.com", "tfe.example.com"},
		// IPv6 literal — left alone (we don't try to parse the port out
		// of `[::1]:443` here; that path isn't reached by the YAML loader
		// and Go's URL parser keeps the brackets attached to Host).
		{"https", "[::1]:443", "[::1]:443"},
		// Empty scheme — passthrough (we only have schemes when a URL
		// was parsed; bare host:port lookups stay as-is).
		{"", "tfe.example.com:443", "tfe.example.com:443"},
	}
	for _, c := range cases {
		if got := stripDefaultPort(c.scheme, c.in); got != c.want {
			t.Errorf("stripDefaultPort(%q, %q) = %q, want %q",
				c.scheme, c.in, got, c.want)
		}
	}
}
