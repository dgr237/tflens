package resolver

import "strings"

// registrySource is a parsed Terraform-registry module source string of
// the form:
//
//	[host/]namespace/name/provider[//subdir]
//
// When host is omitted the public registry (registry.terraform.io) is
// implied — callers fill that default in.
type registrySource struct {
	host     string // empty means "use the default registry host"
	ns       string
	name     string
	provider string
	subdir   string // the "//subdir" portion, without the leading slashes
}

// parseRegistrySource attempts to parse s as a registry source. The second
// return value is false when s is clearly not a registry source — a local
// path, a forced scheme (git::..., http://...), or a known VCS shorthand
// host (github.com/foo/bar is a git source, not a registry source).
//
// A returned registrySource with empty host means the caller should use
// the configured default registry host.
func parseRegistrySource(s string) (registrySource, bool) {
	if s == "" {
		return registrySource{}, false
	}
	if strings.HasPrefix(s, "./") || strings.HasPrefix(s, "../") {
		return registrySource{}, false
	}
	if strings.Contains(s, "::") || strings.HasPrefix(s, "git@") {
		return registrySource{}, false
	}
	if strings.Contains(s, "://") {
		return registrySource{}, false
	}

	main, subdir, _ := strings.Cut(s, "//")
	parts := strings.Split(main, "/")

	switch len(parts) {
	case 3:
		// ns/name/provider — but github.com/foo/bar and similar VCS
		// shorthand also have 3 parts; reject those here.
		if isKnownVCSHost(parts[0]) {
			return registrySource{}, false
		}
		return registrySource{
			ns:       parts[0],
			name:     parts[1],
			provider: parts[2],
			subdir:   subdir,
		}, validSegments(parts)
	case 4:
		// host/ns/name/provider — the host segment must look like a
		// hostname (contain a dot) to disambiguate from garbage.
		if !strings.Contains(parts[0], ".") {
			return registrySource{}, false
		}
		return registrySource{
			host:     parts[0],
			ns:       parts[1],
			name:     parts[2],
			provider: parts[3],
			subdir:   subdir,
		}, validSegments(parts)
	default:
		return registrySource{}, false
	}
}

// validSegments rejects sources with empty segments (e.g. "ns//provider"
// before the subdir cut) or segments containing whitespace.
func validSegments(parts []string) bool {
	for _, p := range parts {
		if p == "" {
			return false
		}
		if strings.ContainsAny(p, " \t\n") {
			return false
		}
	}
	return true
}

// isKnownVCSHost reports whether the given segment is a well-known VCS
// host used in Terraform's "VCS shorthand" source form (e.g.
// "github.com/org/repo"). These are handled by the git resolver, not the
// registry resolver.
func isKnownVCSHost(segment string) bool {
	switch segment {
	case "github.com", "bitbucket.org", "gitlab.com", "codeberg.org":
		return true
	}
	return false
}
