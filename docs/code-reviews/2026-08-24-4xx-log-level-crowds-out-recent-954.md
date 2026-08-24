# Code review: routine 4xx errors no longer crowd out real problems in Recent()/ADR-0018 feed (ut-docs#954)

**Date:** 2026-08-24
**Author:** autonomous pipeline (Dev: Sonnet, inline; Review: Opus, worktree-isolated subagent — `complexity:medium` per the scrum-master skill's model routing)
**Issue:** universaltill/ut-docs#954 (follow-up to #947)

## What shipped

`common.LogAndLocalizedError` (`internal/pages/common/errors.go`) always
logged at Error level regardless of the HTTP status it was reporting. Since
#947 routed it through `internal/logging`'s leveled logger, every Error-level
call also landed in `logging.Recent()` — the in-memory Problems ring (cap 50)
that powers the backoffice "recent problems" panel and the cloud-sync
heartbeat digest (cap 20, ADR-0018). Of 107 call sites, ~20 report 4xx
statuses that are routinely operator-triggerable (a malformed form, a
declined tender) — not real server problems. A cashier fat-fingering a form
repeatedly could evict genuine warnings from both capped, priority-blind
buffers.

- `internal/pages/common/errors.go`: `LogAndLocalizedError` now derives the
  log level from `status` — `>= http.StatusInternalServerError` (500) still
  logs at Error and reaches `logging.Recent()` exactly as #947 shipped it;
  anything below logs at Info instead, which `logging.remember()`'s existing
  `level < Warn` cutoff already excludes from the ring, so it doesn't
  compete with a real 5xx for a slot. No call sites needed editing — the
  branch lives inside the shared helper, so all ~107 callers (including one
  dynamic-status call site, `catalog/handlers.go`'s `skuAwareError`) get
  correct behavior automatically.
- `internal/pages/common/errors_test.go`: two new tests —
  `TestLogAndLocalizedError4xxDoesNotPolluteRecent` (a 400 call leaves
  `logging.Recent()` empty; response status/body unchanged) and
  `TestLogAndLocalizedError5xxStillReachesRecentAmid4xxNoise` (a mix of
  400/500/402 calls leaves exactly the 500 one in `Recent()`, at ERROR).
- `make docs-shots` re-run; `web/help/img/manifest.json` refreshed (see
  below — required even though this change touches no UI).

## Independent review (Opus, worktree-isolated)

**Initial verdict: NOT SAFE TO MERGE** — one blocking finding, three
non-blocking/informational.

### Blocking (fixed)

**`guard-docs-shots.sh` failed.** `internal/pages/common/errors.go` is under
the guard's scanned surface (`internal/pages/**.go`) even though it registers
no routes — it falls in the guard's deliberately-kept "shared helper" bucket
(`guard-docs-shots_test.sh` has an explicit regression case for exactly this
class). Fixed: `make docs-shots` re-run, `web/help/img/manifest.json`'s
surface hash updated. Two screenshots (`ar/translations.png`,
`fa/translations.png`) also picked up a handful of changed bytes from
anti-aliasing noise in that run — pixel-diffed against the prior versions
(cropped to the diff bounding box) and confirmed visually identical, not a
real content change.

### Non-blocking (fixed)

**Doc comment overclaimed 4xx log visibility.** The comment said a 4xx call
stays "logged and grepable server-side" unconditionally; in fact `UT_LOG_LEVEL`
is operator-settable, and under `warn`/`error` an Info-level 4xx line is
filtered out by the logger's own existing level check — same as any other
Info line, not special-cased here, but the comment didn't say so. Fixed:
comment now notes this depends on the default `info` level.

### Non-blocking / informational (accepted, not fixed here)

- `sync_api.go:261` reports `advertisableHost` failure (till can't determine
  its own LAN address — a config/environment fault, not an operator typo) as
  `http.StatusConflict`, so it now logs at Info like any other 4xx. If this
  particular case should stay visible in the Problems feed, the fix belongs
  at that call site (a different status, or a direct `Errorf`), not in the
  threshold — noted for a future card if it turns out to matter in practice.
- `internal/issuereport/bundle.go` embeds `logging.Recent()` into bug-report
  bundles; a 4xx-producing flow an operator reports on will no longer have
  that line attached to the bundle. Accepted tradeoff — the whole point of
  this fix is that a 4xx isn't Problems-feed-worthy.

### Design correctness (confirmed independently)

- Threshold `status >= http.StatusInternalServerError` reviewed against the
  actual call-site tally (500×83, 400×17, 402×2, 502×2, dynamic×1, 404×1,
  409×1): 502 (upstream unreachable) correctly stays Error; the one dynamic
  caller only ever passes 400 at runtime, and the branch is a plain int
  comparison so it's correct for any value regardless.
- No externally-observable behavior changed: `LocalizedError(w, r, status,
  key)` sits unchanged, outside the branch, in the same position — response
  status/body/translated key are identical to before. Asserted directly by
  both new tests.

## Verified beyond automated tests

- **TDD claim re-verified independently** (worktree-isolated, not the
  orchestrator's shared checkout): fix reverted with the new tests kept →
  both new tests fail for the claimed reason (`[ERROR]` lines on routine 4xx,
  `Recent()` non-empty when it should be empty / has 3 entries instead of 1).
  Fix restored → all four tests in the file pass, `[INFO]` lines confirm 4xx
  is still emitted at the default level. Full output captured in the review
  transcript.
- `go build ./...`, `go vet ./...`, `gofmt -l .` — clean.
- `go test ./...` (module-wide) — green, including `internal/plugins` run
  separately with `-timeout 20m` (matches `ci.yml`'s own split — that package
  needs longer than the default 600s under load, unrelated to this diff; a
  first `-race` gate run without the extended timeout hit exactly that,
  confirmed as a gate-invocation artifact, not a regression, by re-running
  with CI's actual command).
- Every CI-blocking guard: `guard-data-access.sh`, `guard-kiosk-engine.sh`,
  `guard-plugin-menu-read.sh`, `guard-i18n.sh`, `guard-compliance-claims.sh`,
  `guard-docs-shots.sh`, `guard-help-topics.sh` — all pass.
- No SQL in the diff, no money handling, no new user-facing string (server-
  side log level only — response body/status unchanged), no secrets or real
  client/shop names.
- Checked for this pipeline's two recurring bug classes — neither applies:
  no file-write handler (no `os.MkdirAll`/`os.Create`/`os.WriteFile` in the
  diff), no cwd-relative path construction (the one `filepath.Join` in the
  test file is pre-existing fixture-loading code, untouched by this commit).

## Safe-to-merge verdict

**Safe to merge** after the docs-shots regeneration and comment fix above.
Both the design (deriving level from status, using the ring's own existing
`Warn` cutoff rather than adding new priority logic) and the tests (proven
to fail without the fix, for the right reason) are sound.

## Explicitly deferred

- `sync_api.go`'s `advertisableHost` 409 case potentially wanting Error-level
  visibility on its own merits — separate from this task's scope; a future
  card if it proves to matter operationally.
