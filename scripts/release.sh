#!/usr/bin/env bash
#
# release.sh — promote CHANGELOG [Unreleased] to a versioned section,
# commit the change, create an annotated git tag, and (with --push)
# push both the commit and the tag.
#
# Usage:
#   scripts/release.sh <version> [--push]
#
# Example:
#   scripts/release.sh 0.1.4 --push
#
# Pre-conditions:
#   - On the main branch
#   - Working tree clean
#   - The [Unreleased] section in CHANGELOG.md has at least one entry
#   - The supplied version doesn't already exist as a tag
#
# What it does:
#   1. Verifies pre-conditions
#   2. Renames `## [Unreleased]` to `## [<version>] — <today>`
#   3. Inserts a fresh empty `## [Unreleased]` section above
#   4. Updates the comparison-link footer (Unreleased → previous → new)
#   5. git add CHANGELOG.md && git commit -m "Release v<version>"
#   6. git tag -a v<version> -m "<extracted CHANGELOG section>"
#   7. With --push: git push origin main && git push origin v<version>
#
# The tag annotation reuses the CHANGELOG section verbatim so the
# release-notes workflow has a single source of truth.

set -euo pipefail

usage() {
    echo "Usage: $0 <version> [--push]" >&2
    echo "  <version>: semver without the leading 'v' (e.g. 0.1.4)" >&2
    echo "  --push:    push the commit + tag to origin after creating them" >&2
    exit 2
}

if [[ $# -lt 1 || $# -gt 2 ]]; then usage; fi
VERSION="${1#v}" # strip a leading v if the caller included one
PUSH="${2:-}"
if [[ -n "$PUSH" && "$PUSH" != "--push" ]]; then usage; fi

if [[ ! "$VERSION" =~ ^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?$ ]]; then
    echo "error: version $VERSION is not a valid semver" >&2
    exit 1
fi

REPO_ROOT="$(git rev-parse --show-toplevel)"
cd "$REPO_ROOT"

# Pre-condition checks -------------------------------------------------

if [[ "$(git rev-parse --abbrev-ref HEAD)" != "main" ]]; then
    echo "error: must be on main branch (currently on $(git rev-parse --abbrev-ref HEAD))" >&2
    exit 1
fi

if [[ -n "$(git status --porcelain)" ]]; then
    echo "error: working tree is not clean" >&2
    git status --short >&2
    exit 1
fi

if git rev-parse -q --verify "refs/tags/v${VERSION}" >/dev/null; then
    echo "error: tag v${VERSION} already exists" >&2
    exit 1
fi

if [[ ! -f CHANGELOG.md ]]; then
    echo "error: CHANGELOG.md not found at repo root" >&2
    exit 1
fi

# Sentinel marker check ----------------------------------------------
#
# The <!-- unreleased-anchor --> marker must live inside [Unreleased].
# Promotion anchors on this marker (not on `## [Unreleased]`) so that
# a PR diff which adds entries below it still lands under [Unreleased]
# after a merge that crosses a release boundary. Without the marker,
# git's three-way merge can place new entries under the just-promoted
# ## [vX.Y.Z] section instead — the failure mode we saw on v0.17→v0.18
# and v0.18→v0.19.
#
# Failing loudly here is intentional: silently degrading to a release
# with empty notes is worse than blocking until the marker is restored.
if ! awk '
    /^## \[Unreleased\]/ { in_unrel = 1; next }
    in_unrel && /^## \[/ { in_unrel = 0 }
    in_unrel && /^<!-- unreleased-anchor/ { found = 1; exit }
    END { exit found ? 0 : 1 }
' CHANGELOG.md; then
    echo "error: [Unreleased] section is missing the <!-- unreleased-anchor --> marker" >&2
    echo "  add the following line directly under '## [Unreleased]' in CHANGELOG.md:" >&2
    echo "    <!-- unreleased-anchor — leave this line in place; PR entries go below it -->" >&2
    exit 1
fi

# Confirm Unreleased has content -------------------------------------
#
# Strip blank lines AND the sentinel marker from the section body so a
# section containing only the sentinel reads as empty.
UNRELEASED_BODY="$(awk '
    /^## \[Unreleased\]/ { in_section=1; next }
    in_section && /^## \[/ { in_section=0 }
    in_section { print }
' CHANGELOG.md | sed -e '/^[[:space:]]*$/d' -e '/^<!-- unreleased-anchor/d')"

if [[ -z "$UNRELEASED_BODY" ]]; then
    echo "error: [Unreleased] section is empty — nothing to release" >&2
    exit 1
fi

# Pick the previous version tag by sorting all v*.*.* tags semver-style.
# Using `tag -l --sort=-v:refname` rather than `git describe` because
# describe requires the previous tag to be reachable from HEAD, which
# isn't always true (e.g. shallow clones, branch divergence). All we
# need here is "what's the latest existing version tag".
PREVIOUS_TAG="$(git tag -l 'v*' --sort=-v:refname | head -n 1)"
TODAY="$(date -u +%Y-%m-%d)"

# Promote --------------------------------------------------------------
#
# Insert `## [<version>] — <today>` directly after the sentinel line.
# The `## [Unreleased]` header itself stays put; the sentinel stays put
# under it. Everything that was previously between the sentinel and the
# next section (the entries) becomes the body of the new versioned
# section. Anchoring on the sentinel — a unique, stable string — is
# what protects merges that cross a release boundary from landing in
# the wrong place; see the sentinel marker check above.

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

awk -v ver="$VERSION" -v today="$TODAY" '
    /^<!-- unreleased-anchor/ {
        print
        print ""
        print "## [" ver "] — " today
        next
    }
    { print }
' CHANGELOG.md > "$tmp"

# Update the comparison-link footer.
#
# Replace:
#   [Unreleased]: .../compare/v<previous>...HEAD
# With:
#   [Unreleased]: .../compare/v<version>...HEAD
#   [<version>]: .../compare/v<previous>...v<version>
#
# Using awk instead of sed -e "s|...|...\n...|": GNU sed expands `\n`
# in the replacement to a newline, BSD sed (macOS) emits a literal `n`.
# awk's `print` is portable across both.
if [[ -n "$PREVIOUS_TAG" ]]; then
    awk -v prev="$PREVIOUS_TAG" -v ver="$VERSION" '
        $0 ~ "^\\[Unreleased\\]: " && index($0, "/compare/" prev "...HEAD") {
            url = $0
            sub(/^\[Unreleased\]: /, "", url)
            sub(/\/compare\/.*$/, "", url)
            print "[Unreleased]: " url "/compare/v" ver "...HEAD"
            print "[" ver "]: " url "/compare/" prev "...v" ver
            next
        }
        { print }
    ' "$tmp" > "$tmp.new" && mv "$tmp.new" "$tmp"
fi

mv "$tmp" CHANGELOG.md
trap - EXIT

# Extract the new section's body for the tag annotation ---------------
#
# Trim leading/trailing blank lines in awk rather than `sed '1{/^$/d}'`:
# BSD sed (macOS) rejects the GNU shorthand with "extra characters at
# the end of d command". The portable POSIX form splits the brace
# block across lines, which is fiddly to embed in a here-string; awk
# is cleaner and works on both.
TAG_BODY="$(awk -v ver="$VERSION" '
    $0 == "## [" ver "] — '"$TODAY"'" { in_section=1; next }
    in_section && /^## \[/ { in_section=0 }
    in_section {
        if (!started && NF == 0) next
        started = 1
        if (NF == 0) {
            pending++
        } else {
            while (pending-- > 0) print ""
            pending = 0
            print
        }
    }
' CHANGELOG.md)"

# Commit + tag --------------------------------------------------------
git add CHANGELOG.md
git commit -m "Release v${VERSION}" --quiet

git tag -a "v${VERSION}" -m "v${VERSION}

${TAG_BODY}"

echo "Created v${VERSION} on $(git rev-parse --short HEAD)"
echo

if [[ "$PUSH" == "--push" ]]; then
    git push origin main
    git push origin "v${VERSION}"
    echo "Pushed v${VERSION} to origin"
else
    echo "Local only. To publish:"
    echo "  git push origin main"
    echo "  git push origin v${VERSION}"
fi
