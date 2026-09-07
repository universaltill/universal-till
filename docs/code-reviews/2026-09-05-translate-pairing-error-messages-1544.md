# Code review: translate the 6 remaining hardcoded pairing-error strings (ut-docs#1544)

**Date:** 2026-09-05
**Branch:** `fix/1544-translate-pairing-error-messages`
**Complexity:** easy (Dev: Sonnet inline, Review: fresh-context Sonnet subagent)

## What shipped

Follow-up to ut-docs#1540 (universal-till#766), which translated one of the
seven hardcoded English strings in the outbound till-pairing flow
(`pairStartHandler`/`pairStatusHandler`/`friendlyJoinError`) and left the
other six. All six reach the operator through `pairWaitView`'s `errMsg`
field → `pairing_wait.html`'s `{{ .errMsg }}` — a raw Go string handed to
the template as **data**, not a `{{ T "key" }}` call — which is exactly why
`guard-i18n.sh`'s template/response/toast checks never caught them.

- `internal/pages/pairing_join.go`: `pairStartHandler` resolves `locale`
  once at the top (previously only inside the missing-name branch) and
  reuses it in every error branch; `pairStatusHandler` does the same. All 5
  of that file's targeted strings now route through `httpx.T`:
  `tills.pairing.error.invalid_address`, `.unreachable` (`%s` = the network
  error), `.refused`, `.unexpected_response`, `.unexpected_response_status`
  (`%s` = `resp.Status`).
- `internal/pages/sync_api.go`: `friendlyJoinError`'s defensive fallback for
  a non-`*joinError` (previously `return err.Error()`, raw English) now
  returns `fmt.Sprintf(httpx.T(locale, "tills.join_error.unexpected"), err.Error())`.
- New keys added to `web/locales/{en,ar,fa,tr}.json` (6 keys, real
  translations in each, not English copies) **and** to the external
  `ut-plugin-language-{de,es}` packs in the same cycle (see "Language pack
  follow-up" below) — `lang-pack-drift` is blocking on push to `main`.
- Updated the 4 existing tests that asserted on the old raw-English
  substrings (`TestPairStart_SurfacesUnreachablePrimary`,
  `TestPairStart_RejectsInvalidBaseURL`,
  `TestPairStatus_ErrorStateKeepsPollingAndKeepsItsMessage`,
  `TestFriendlyJoinError_FallsBackForUnclassifiedErrors`) to assert against
  the translated text (a static prefix for the two `%s`-carrying messages,
  since the interpolated detail is OS-/response-dependent).

## Independent review (fresh-context Sonnet subagent, isolated worktree)

Ran: `gofmt -l .`, `go build ./...`, `go vet ./...`,
`go test ./internal/pages/...`, full `go test ./...`,
`golangci-lint run ./internal/pages/...`, `guard-i18n.sh`,
`guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-compliance-claims.sh`,
`guard-help-topics.sh` — all clean.

Verified independently (not just trusted): every new key resolves in
`en.json`; `ar`/`fa`/`tr` carry the same 6 keys with real, distinct
translations (none is a copy of the English string); `%s` placeholder
counts match across all 4 locales for every key that has one; `locale` is
resolved exactly once per handler and reused consistently.

**TDD re-verified independently**, twice, atomically (revert → test →
restore with no turn boundary in between): reverted `pairing_join.go` +
`sync_api.go` to `main`, confirmed all 4 updated tests fail for the right
reason (asserting the OLD raw-English text is what's now missing), restored,
confirmed green again.

## Findings

1. **(Low, deferred to ut-docs#1611)** `pairStartHandler`'s
   `http.NewRequestWithContext` error branch (`pairing_join.go:214`) still
   renders raw `err.Error()` untranslated — same defect class, not one of
   this card's 6 targeted strings, realistically unreachable (URL already
   validated, method/reader are compile-time-safe). Not fixed here — out of
   this card's scope; tracked as a new Backlog card.
2. **(Low, deferred to ut-docs#1612)** The updated tests only exercise the
   `en` locale end-to-end through `pairStartHandler`/`pairStatusHandler` —
   no test drives `?lang=ar/fa/tr` through those two handlers specifically
   (only `friendlyJoinError`'s separate code path has multi-locale
   coverage, `TestFriendlyJoinError_TranslatesEachKind`). Confirmed by
   reading the code that the implementation is correct regardless; this is
   a coverage gap for future regressions, not a live bug. Tracked as a new
   Backlog card.
3. No other findings. Repo-wide grep for the original raw-English
   substrings found only unrelated comments. No `web/help/` topic quotes
   the old English text (checked `multitill.md`, the only pairing-adjacent
   manual page) — no manual update needed, since nothing an operator does
   or sees changed (same message, now in their own language). No client/
   shop name or secret-shaped literal in the diff.

## Language pack follow-up (ut-docs#1576's own warning, applied)

Before merging, the 6 new `en.json` keys were translated and committed on
matching branches in `ut-plugin-language-de` and `ut-plugin-language-es`
(`i18n/1544-pairing-error-keys`), verified against this branch's own
`en.json` via `UT_CORE_EN_JSON=<local path>`
`scripts/check-key-drift.sh` in each pack — both report full parity, 0
drift, 0 orphans, 0 token mismatches. Those PRs are opened and merged in
the same cycle as this one (immediately after this PR merges, since their
own CI fetches core's live `main`), per the scrum-master skill's "lane that
merges the core change owns the implied follow-up" rule.

## Verdict

**Safe to merge.** No blocking findings.
