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

# Confirm Unreleased has content -------------------------------------
UNRELEASED_BODY="$(awk '
    /^## \[Unreleased\]/ { in_section=1; next }
    in_section && /^## \[/ { in_section=0 }
    in_section { print }
' CHANGELOG.md | sed '/^[[:space:]]*$/d')"

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
# The substitutions below are deliberately separate so a future reader
# can read each one in isolation. Order matters: we add the new empty
# Unreleased section AFTER renaming the existing one, otherwise the
# replace pattern would match the new section instead.

tmp="$(mktemp)"
trap 'rm -f "$tmp"' EXIT

awk -v ver="$VERSION" -v today="$TODAY" '
    # Rename the existing [Unreleased] to [<version>] — <today>
    /^## \[Unreleased\]/ {
        print "## [Unreleased]"
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
if [[ -n "$PREVIOUS_TAG" ]]; then
    sed -i.bak \
        -e "s|^\[Unreleased\]: \(.*\)/compare/${PREVIOUS_TAG}\.\.\.HEAD|[Unreleased]: \1/compare/v${VERSION}...HEAD\n[${VERSION}]: \1/compare/${PREVIOUS_TAG}...v${VERSION}|" \
        "$tmp"
    rm -f "${tmp}.bak"
fi

mv "$tmp" CHANGELOG.md
trap - EXIT

# Extract the new section's body for the tag annotation ---------------
TAG_BODY="$(awk -v ver="$VERSION" '
    $0 == "## [" ver "] — '"$TODAY"'" { in_section=1; next }
    in_section && /^## \[/ { in_section=0 }
    in_section { print }
' CHANGELOG.md | sed -e '1{/^$/d}' -e '${/^$/d}')"

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
