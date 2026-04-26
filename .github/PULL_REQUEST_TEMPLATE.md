<!--
Thanks for contributing! Fill in the sections below — keep it terse, the
PR description shows up in the auto-release notes if you label this PR
with release:patch / minor / major.

Delete this comment block before submitting.
-->

## Summary

<!-- 1–3 bullets. What changed and why. Link the issue if there is one. -->

-

## Test plan

<!-- Checkbox list of how you verified the change. Real commands you ran,
     not aspirational ones. -->

- [ ] `make check` passes
- [ ]

## CHANGELOG

<!-- User-visible changes need an entry under `## [Unreleased]` in
     CHANGELOG.md (grouped by Added / Changed / Deprecated / Removed /
     Fixed / Security).

     If the change is purely internal (refactor, test-only, dep bump
     with no behaviour change, etc.), apply the `no-changelog` label
     instead. The changelog-check workflow auto-skips PRs that only
     touch tests / testdata / .github / scripts / top-level Markdown. -->

- [ ] CHANGELOG.md updated, OR `no-changelog` label applied, OR all changed files are in the auto-skip list.

## Release label

<!-- If this is user-facing and ready to ship on merge, apply ONE of:

     - `release:patch` — bug fix, doc fix, internal cleanup with no API change
     - `release:minor` — backwards-compatible feature add
     - `release:major` — breaking change

     PRs without a release label are silently skipped by auto-release —
     `[Unreleased]` entries accumulate until the next release-labelled
     merge. That's fine for a series of small changes you want to ship
     together. -->
