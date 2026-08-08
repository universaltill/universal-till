# Code review: i18n the multi-till join error messages

**Ticket:** universaltill/ut-docs#36
**Date:** 2026-08-08
**Repo/branch:** `universal-till` / `fix/i18n-multi-till-join-errors-36`
**Reviewer:** independent Sonnet subagent (complexity:easy tier — fresh-context
Sonnet is the review model for `easy`), isolated worktree

## What shipped

`joinPrimary`/`completeJoin` in `internal/pages/sync_api.go` (the replica-side
"join a shop via QR/pairing-code" flow) built every failure message as a
hardcoded English `fmt.Errorf(...)`, and the `/api/sync/join` +
`/api/setup/join` handlers rendered `err.Error()` straight into the HTML
response — so a mis-pasted pairing code showed an English error even on an
ar/fa/tr till, while the success path (`tills.joined`/`tills.restart_to_finish`)
was already localized via `httpx.T`.

Fix:

- A `joinError` type (`kind` + optional `detail`) is now returned by every
  `joinPrimary`/`completeJoin` failure path instead of `fmt.Errorf(...)`.
  `detail` carries non-translatable dynamic content (a URL, a wrapped network
  error, an HTTP status) — 8 distinct kinds cover every existing return path
  (`bad_code`, `request_failed`, `unreachable`, `not_a_till`, `refused`,
  `snapshot_failed`, `stage_snapshot_failed`, `stage_identity_failed`).
- `friendlyJoinError(locale, err)` classifies via `errors.As`, looks up the
  locale key via `httpx.T`, and — when the error carries a `detail` —
  substitutes it into the locale string's `%s` placeholder via `fmt.Sprintf`,
  mirroring the existing convention at
  `internal/pages/common/barcode_conflict.go`
  (`fmt.Sprintf(httpx.T(locale, "catalog.error.barcode_conflict"), label)`).
- `/api/sync/join` and `/api/setup/join` now call `friendlyJoinError(locale, err)`
  instead of `err.Error()` before `html.EscapeString`.
- `internal/pages/pairing_join.go`'s `pairStatusHandler` (the separate
  approve-to-pair flow, ut-docs#185/ADR-0033) also calls the shared
  `completeJoin` and was rendering `err.Error()` directly — since
  `joinError.Error()` now returns a locale KEY string (not English prose),
  this second call site needed the same `friendlyJoinError` treatment to
  avoid a regression there.
- 8 new locale keys (`tills.join_error.*`) added to all four locale files
  (`en.json`, `ar.json`, `fa.json`, `tr.json`), each translated, with `%s`
  present only where a kind carries a `detail`.
- New tests in `internal/pages/sync_api_test.go`:
  `TestFriendlyJoinError_TranslatesEachKind` (8 kinds × 4 locales),
  `TestFriendlyJoinError_FallsBackForUnclassifiedErrors` (defensive fallback
  for a non-`*joinError`), `TestSyncJoin_ErrorsAreLocalized` (HTTP-level,
  `?lang=fa`, proves the English substring no longer leaks and the fa
  translation appears). All pre-existing `TestSyncJoin_*` tests pass
  unchanged — the English wording for `unreachable`/`refused`/`snapshot_failed`
  was kept identical to the original `fmt.Errorf` text specifically so their
  existing substring assertions (`"cannot reach"`, `"refused the enrolment"`,
  `"snapshot download failed"`) still hold with no test edits needed.
- Regenerated `web/help/img/**` screenshots + `manifest.json` via
  `make docs-shots` — required because `internal/pages/**.go` changed,
  tripping `guard-docs-shots.sh`'s surface-hash check; no page/screen
  actually changed, this diff touches no template or route.

## Independent review (round 1)

An independent Sonnet subagent, isolated in its own git worktree, reviewed
the diff without having seen any prior reasoning about it:

- Ran `go build ./...`, `go vet ./...`, the targeted
  `go test ./internal/pages/... -run 'Sync|Join|Pairing|FriendlyJoinError' -v`
  (all pass, including all 32 `TestFriendlyJoinError_TranslatesEachKind`
  subtests), the unfiltered `internal/pages`/`catalog`/`common` package
  tests (pass), and all four guards (`guard-i18n.sh`,
  `guard-data-access.sh`, `guard-help-topics.sh`, `guard-docs-shots.sh`) —
  clean.
- **Independently re-verified the TDD claim**: reverted just
  `internal/pages/sync_api.go` and `internal/pages/pairing_join.go` to their
  pre-fix state, confirmed the new test file failed to *compile*
  (`undefined: joinError`, `undefined: joinErrBadCode`, …) — the expected
  failure mode for new plumbing rather than a runtime assertion — then
  restored both files and confirmed clean build + green tests again.
- **Independently verified locale parity and the `%s`-placeholder
  convention** across all four locale files for all 8 new keys: every key
  whose Go-side kind carries a `detail` has exactly one `%s` in every
  locale's value; the two detail-less kinds (`bad_code`, `refused`) have
  none, in any locale. Also sanity-checked the ar/fa/tr translations
  themselves as genuine, well-formed, non-garbled translations with `%s`
  placed naturally for each sentence.
- **Independently checked the regenerated screenshots**: only
  `alerts.png`/`designer.png` (all locales), `ar/users.png`, `tr/sell.png`,
  and `manifest.json` changed; none of those topics' routes are touched by
  this diff's actual code. Visually diffed `en/alerts.png` before/after —
  the only difference is the live wall-clock log timestamp baked into the
  "Recent problems" panel, the same known noise class as
  universaltill/ut-docs#360/#370, not a real UI regression.
- Checked the standing rules: no filesystem I/O touched (so neither of the
  two recurring bug classes — missing `os.MkdirAll`, a cwd-relative path
  instead of `paths.Data(...)` — applies), no secret-shaped literal or real
  client/shop name introduced anywhere in the diff.
- Judged the new `joinErrRequestFailed` kind (covering the two
  `http.NewRequestWithContext` construction-error paths, previously bare
  `return "", err`): reachable in principle (a pasted pairing code's
  `primaryURL` has no URL-shape validation before this point, unlike the
  mDNS pairing flow's `validPrimaryBaseURL`), but strictly no new failure
  surface — the same raw text now rides behind a translated prefix instead
  of leaking untranslated.
- Confirmed `pairing_join.go`'s new `httpx.ResolveLocale(w, r)` call is
  safe: called before any header/body write in `pairStatusHandler`, so no
  "headers already sent" risk.

**Verdict: no blocking findings.**

### Non-blocking finding — accepted as-is, not fixed

- `internal/pages/pairing_join.go`'s new error branch calls
  `httpx.ResolveLocale(w, r)` explicitly, and `pairWaitView` →
  `httpx.RenderPartial` calls it again internally — `ResolveLocale` runs
  twice per request on this path. Functionally harmless (at most an
  identical duplicate `Set-Cookie` when `?lang=` is present); fixing it
  cleanly would mean changing `RenderPartial`'s shared signature to accept
  a pre-resolved locale, which is out of scope for an easy-tier,
  narrowly-scoped i18n fix and would touch every other `RenderPartial`
  call site in the codebase. Left as-is; noted here rather than silently
  dropped.

## Verified beyond automated tests

- Full `go test ./... -race` run once, after implementation was finished:
  every package green except `internal/issuereport`'s
  `TestSaveCleansUpDirectoryOnWriteFailure`, the same pre-existing,
  already-tracked, unrelated failure documented in universaltill/ut-docs#258/#415
  (sandbox runs tests as root, so the read-only-directory precondition
  never actually fails) — reproduces identically on `main`, not a
  regression here.
- `make docs-shots` run for real against two live till servers (default +
  auth-gated), all 56 screenshots regenerated and visually spot-checked
  (`en/alerts.png`, `en/designer.png`, `tr/sell.png`, `ar/users.png`) —
  correct rendering, no missing fonts/truncation/layout breakage; the only
  differences are the expected live timestamp/seed content, same as noted
  above.
- No new page or route added — no manual/help-topic prose needed updating,
  and `guard-help-topics.sh` stayed green.

## Safe to merge

Yes. Build, vet, the full non-flaky test suite, all four CI guards, and an
independent adversarial re-verification (revert-then-restore TDD check,
locale-parity/%s-placeholder cross-check, screenshot-diff sanity check, the
two recurring-bug-class checklist) all pass or confirm the diff's own
claims. No blockers found; one cosmetic finding accepted as-is with the
reasoning recorded above.
