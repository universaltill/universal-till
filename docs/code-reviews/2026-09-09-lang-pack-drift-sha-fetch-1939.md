# lang-pack-drift: fetch pack content by commit SHA, not by `main` (ut-docs#1939)

## What shipped

`scripts/ci/check-lang-pack-drift.sh` fetched each language pack's content
(its `check-key-drift.sh`, locale JSON, and baseline files) from
`raw.githubusercontent.com/universaltill/<repo>/main/<path>` — a
branch-ref raw URL that is CDN-fronted with a short (~5 min) per-edge TTL
and can serve stale content shortly after a pack PR merges. The script
separately resolved the pack's latest commit SHA via the GitHub API
(always strongly consistent) but only used it for a log annotation, never
for the actual fetch. The result: a failure message could name a SHA that,
if checked out directly, already contains the "missing" key — a real
incident, reproduced and documented in the issue (`fiscaldevice.error.
owner_required_country_change` reported missing from
`ut-plugin-language-de@bc89c7b6`, though `git show bc89c7b6:locales/
de.json` contains it).

The fix: `fetch()` now takes an explicit git ref and the script resolves
`ref="$pack_sha"` before fetching, falling back to `main` only when the
SHA lookup itself failed (network/rate-limit — same as before this
change, not a new failure mode). A SHA-addressed raw URL is immutable, so
the printed SHA and the fetched content are now causally the same thing.

## Independent review (Sonnet, fresh-context subagent — easy tier)

Verdict: **safe to merge, no blocking defects.**

- Confirmed all 4 `fetch()` call sites pass the new `ref` argument.
- Independently verified the `ref="$pack_sha"; [ "$ref" = "unknown" ] &&
  ref="main"` fallback is safe under `set -e` (a `test && assignment` pair
  positionally non-last in an AND-list is errexit-exempt) with a
  standalone repro, not just by reading.
- Traced the "pack unreachable" test case end-to-end: `commits/main` still
  resolves (so `ref` still becomes the SHA), and `fetch` still 404s
  against that SHA path since no content exists there — correct hard
  failure, matches the test.
- Confirmed the exact divergence bug is now structurally impossible: when
  SHA resolution succeeds, the printed SHA and the fetched `ref` are the
  literal same string; when it fails, the log honestly says `unknown`
  rather than printing a fabricated SHA.
- Reviewed the test refactor (`fixture_pack` split into
  `_write_pack_content` + `fixture_pack`) and confirmed cases 1-4 still
  exercise the same sync/drift/unreachable semantics, now under a
  SHA-named fixture directory instead of a hardcoded `main` one, matching
  the script's new fetch behavior.
- No `mkdir -p` gap, no new cwd-relative path assumption, no secret or
  real client/shop name introduced.

One nice-to-have noted, explicitly out of scope for this diff:
`.github/workflows/lang-pack-drift.yml`'s own comment describing the
executed pack script as fetched "from each pack repo's own `main` branch"
is now slightly imprecise (the pack's `check-key-drift.sh` is normally
fetched by the resolved SHA after this change) — worth a one-line
touch-up whenever that file is next edited; not blocking, and that file
isn't touched here.

## TDD claim independently re-verified

Reverted just `check-lang-pack-drift.sh`'s fix (kept the new test-file
fixtures, which now write content under a SHA-named directory), re-ran
`check-lang-pack-drift.test.sh`: 5 of 6 cases failed, including both new
regression cases (case 5's `main`-path content is drifted on purpose, so
without the fix the un-fixed script reads it and reports drift; case 6's
content exists only under `main`, but without the fix `pack_sha` is
printed yet content-fetch still hits the *literal* `main` path — with the
new fixture layout the un-fixed script 404s on every SHA-directory read
for the other, `sync`-mode pack instead, since those content dirs are now
SHA-named). Restored the fix, all 6 cases pass again.

## Verified beyond automated tests

- `bash -n` syntax-checked both files.
- `shellcheck` is not installed in this environment and is not yet
  CI-enforced (ut-docs#1943 tracks adding it) — skipped; nothing in the
  diff suggested a shellcheck-class issue on manual read.
- No Go files touched — `gofmt`/`go build`/`go test`/`golangci-lint` are
  unaffected by this change and were not re-run.
- Confirmed no real client/shop name and no secret-shaped literal in the
  diff (only generic fixture repo names and synthetic test SHAs).

## Safe to merge

Yes.
