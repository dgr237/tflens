package resolver

import (
	"net/url"
	"strings"
)

// gitSource is the parsed, normalised form of a git module source. It
// splits the source string into the pieces each downstream consumer
// cares about: the URL to hand to `git clone`, the cache-key components
// (host, path, ref), and the //subdir that selects a directory within
// the cloned tree.
type gitSource struct {
	cloneURL string // full URL ready for `git clone`
	host     string // for cache keying; "_local" for file:// URLs
	path     string // repo path on the host, e.g. "foo/bar.git"
	ref      string // git ref ("v1.0.0", "main", SHA); "" means default branch
	subdir   string // "//subdir" portion without leading slashes
}

// parseGitSource recognises every git module source form we support:
//
//	git::https://github.com/foo/bar.git?ref=v1
//	git::ssh://git@github.com/foo/bar.git//subdir?ref=v1
//	git::file:///tmp/repo.git?ref=v1           (primarily for tests)
//	github.com/foo/bar?ref=v1                  (VCS shorthand)
//	bitbucket.org/foo/bar//sub?ref=v1
//
// The second return value is false for anything we do not recognise —
// local paths, registry sources, plain HTTP URLs without `git::`, etc.
// SCP-style URLs (git@host:path) are not recognised; use the explicit
// `git::ssh://` form instead.
func parseGitSource(s string) (gitSource, bool) {
	if s == "" {
		return gitSource{}, false
	}
	forced := strings.HasPrefix(s, "git::")
	if forced {
		s = strings.TrimPrefix(s, "git::")
	} else {
		// Without a `git::` prefix, we only accept known VCS-host
		// shorthand sources (github.com/, bitbucket.org/, ...).
		firstSeg := s
		if i := strings.IndexAny(s, "/?"); i >= 0 {
			firstSeg = s[:i]
		}
		if !isKnownVCSHost(firstSeg) {
			return gitSource{}, false
		}
		s = "https://" + s
	}

	if !strings.Contains(s, "://") {
		return gitSource{}, false
	}

	urlPart, rawQuery, _ := strings.Cut(s, "?")

	// //subdir separates the repo path from an in-repo subdirectory. It
	// is the FIRST "//" after the scheme's "://", which is why we skip
	// past the scheme before searching.
	schemeEnd := strings.Index(urlPart, "://") + len("://")
	rest := urlPart[schemeEnd:]
	repoPart := rest
	subdir := ""
	if i := strings.Index(rest, "//"); i >= 0 {
		repoPart = rest[:i]
		subdir = rest[i+2:]
	}
	cloneURL := urlPart[:schemeEnd] + repoPart

	u, err := url.Parse(cloneURL)
	if err != nil {
		return gitSource{}, false
	}
	if u.Host == "" && u.Scheme != "file" {
		return gitSource{}, false
	}

	q, _ := url.ParseQuery(rawQuery)

	host := u.Host
	if host == "" {
		host = "_local"
	}
	path := strings.TrimPrefix(u.Path, "/")

	return gitSource{
		cloneURL: cloneURL,
		host:     host,
		path:     path,
		ref:      q.Get("ref"),
		subdir:   subdir,
	}, true
}
