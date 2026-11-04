# Review: help front-matter `public:` flag (ut-docs#5351)

Adds `public: true|false` to the help front matter (parsed by internal/manual; the loader
rejects unknown keys) and sets `public: true` on all 46 topics in the 5 shipped languages.
A test requires every shipped page to declare it and every language to agree with English, so
a new topic is classified on purpose. Consumers: ut-cloud's assistant help index (two-key
publish with its allow-list) and the public help site (ut-website). Reviewed with the
ut-cloud change by an independent Opus pass (finding m1: all 46 are `true`, owner to confirm
the classification). Tests: internal/manual and internal/pages green.

## Follow-up: rebase + docs-shots manifest (2026-11-04)

The PR's `build` check failed in `guard-docs-shots_test.sh`: front matter changed every topic's
markdown hash, so the manifest was stale. `make docs-shots` cannot run on macOS (font pin), and
`public:` is front matter that never reaches the rendered body, so the PNGs are pixel-identical;
only the 128 topic hashes in `web/help/img/manifest.json` were refreshed (scripted sha256 of the
files). Rebased onto main (bug-reporting.md conflict: main's summary kept, `public: true` added).
Independent Opus pass on this delta: sound — only hash values changed, all 128 recomputed and
match, guard + internal/manual + internal/pages green. Verdict: safe to merge.

Second CI failure: guard-help-breadcrumbs_test.sh pinned `tr/updates.md:15`; the added `public:` line shifts it to 16. The test now computes the planted line (verified the guard reports :16: on a manual plant; the sed -i in the test is GNU-only so it cannot run on macOS, CI is the check).
