#!/usr/bin/env bash
#
# test-release-anchor.sh — exercises the sentinel-anchor mechanism in
# scripts/release.sh by simulating the exact cross-release-boundary
# merge that caused the v0.17.0 → v0.18.0 and v0.18.0 → v0.19.0 CHANGELOG
# misattribution bugs.
#
# Scenario:
#   1. Start with a CHANGELOG that has [Unreleased] + sentinel + a prior
#      released section.
#   2. Branch off "main" to a "pr-branch" and add a new entry below the
#      sentinel (simulating an in-flight PR).
#   3. Back on "main", run scripts/release.sh to promote [Unreleased]
#      into a new versioned section (simulating a release happening
#      while the PR is open).
#   4. Merge "pr-branch" into "main" (simulating the PR landing
#      post-release).
#   5. Assert the PR's entry lives under [Unreleased] in the merged
#      result, NOT under the just-promoted versioned section.
#
# Run from anywhere in the tflens repo. Exits 0 on pass, 1 on fail.

set -euo pipefail

REPO_ROOT="$(git rev-parse --show-toplevel)"
RELEASE_SH="$REPO_ROOT/scripts/release.sh"

if [[ ! -x "$RELEASE_SH" ]]; then
    echo "error: $RELEASE_SH is not executable" >&2
    exit 2
fi

TEMPDIR="$(mktemp -d)"
trap 'rm -rf "$TEMPDIR"' EXIT
cd "$TEMPDIR"

# --- set up a fresh repo with a starter CHANGELOG -----------------
git init -q -b main .
git config user.email 'test@example.com'
git config user.name 'Test'

cat > CHANGELOG.md <<'EOF'
# Changelog

## [Unreleased]

<!-- unreleased-anchor — leave this line in place; PR entries go below it -->

### Added

- Feature ready for the next release

## [0.1.0] — 2026-01-01

### Added

- Initial release

[Unreleased]: https://example.com/compare/v0.1.0...HEAD
[0.1.0]: https://example.com/releases/tag/v0.1.0
EOF
git add CHANGELOG.md
git commit -q -m 'Initial commit'
git tag -a v0.1.0 -m 'v0.1.0'

# --- branch off main: simulate a PR adding an Unreleased entry -----
#
# The PR adds a SECOND entry below the sentinel. The existing "Feature
# ready for the next release" entry stays put — it's about to be
# promoted into v0.2.0 by the parallel release below.
git checkout -q -b pr-branch
awk '
    /^<!-- unreleased-anchor/ {
        print
        print ""
        print "- New PR feature"
        next
    }
    { print }
' CHANGELOG.md > CHANGELOG.tmp && mv CHANGELOG.tmp CHANGELOG.md
git add CHANGELOG.md
git commit -q -m 'PR: add Unreleased entry'

# --- back to main: simulate a release of v0.2.0 --------------------
#
# This promotes the "Feature ready..." entry into v0.2.0 and empties
# [Unreleased] (the sentinel stays). Now main and pr-branch have
# divergent CHANGELOGs: pr-branch's "New PR feature" was added below
# the sentinel; main's [Unreleased] has only the sentinel.
git checkout -q main
bash "$RELEASE_SH" 0.2.0 >/dev/null

# --- merge the PR (which carries the pre-release Unreleased layout)
#
# Acceptable outcomes:
#   (a) git auto-merges successfully AND the PR's entry lands under
#       [Unreleased] — sentinel did its job.
#   (b) git reports a conflict — also acceptable: an explicit conflict
#       forces the maintainer to resolve consciously, which is strictly
#       better than the silent mis-attribution we saw without the
#       sentinel (v0.17→v0.18, v0.18→v0.19). The conflict markers will
#       reference the sentinel as the unambiguous anchor.
#
# UNACCEPTABLE: silent auto-merge with the PR's entry under
# [vX.Y.Z] instead of [Unreleased] — the failure mode the sentinel
# exists to prevent.
merge_exit=0
git merge -q pr-branch --no-edit -m 'Merge PR' >/dev/null 2>&1 || merge_exit=$?

if (( merge_exit != 0 )); then
    # Verify the conflict markers reference the sentinel region rather
    # than landing deep inside [vX.Y.Z].
    if grep -qE '^<{7} HEAD$' CHANGELOG.md; then
        echo 'PASS: cross-release merge produced an explicit conflict (acceptable)'
        exit 0
    fi
    echo 'FAIL: merge exited non-zero but no conflict markers in CHANGELOG.md'
    cat CHANGELOG.md
    exit 1
fi

# Auto-merge succeeded — verify the PR entry is in [Unreleased].
section="$(awk '
    /^## \[Unreleased\]/ { in_unrel = 1; print; next }
    in_unrel && /^## \[/ { in_unrel = 0 }
    in_unrel { print }
' CHANGELOG.md)"

if printf '%s' "$section" | grep -qF 'New PR feature'; then
    echo 'PASS: PR entry landed under [Unreleased] after auto-merge'
    exit 0
fi

echo 'FAIL: silent mis-attribution — PR entry landed outside [Unreleased]'
echo '---- merged CHANGELOG.md ----'
cat CHANGELOG.md
echo '---- [Unreleased] section ----'
printf '%s\n' "$section"
exit 1
