# Code review: http.Error raw-leak sweep, increment 4/4 (ut-docs#946, ut-docs#924)

## What shipped

ut-docs#924 tracks raw `http.Error(w, err.Error(), status)` leak sites
across `internal/pages` — the same defect class ut-docs#316/#893 already
fixed dozens of instances of. This is **increment 4, the final slice**:
21 sites across three files.

Files touched: `internal/pages/pairing_api.go` (7 sites),
`plugin_settings_page.go` (7 sites), `sync_api.go` (7 sites). Each site now
routes through `common.LogAndLocalizedError` — logging the real error
server-side and showing the operator a translated message instead, with
the original HTTP status code preserved at every site.

Locale keys: 2 existing keys reused after review (`pairings.error.server`,
`plugins.error.server` — see "Independent review" below for why the first
draft's two new keys were wrong) plus 1 genuinely new key added to all 4
locale files (en/ar/fa/tr): `sync.error.no_lan_address`, for the
LAN-discovery failure at the enrol-QR site (keeps its distinct 409 status,
the one site in `sync_api.go` that isn't the same generic-DB-failure class
as its 6 siblings).

19 new regression tests (one per forced-real-failure call site), each
forcing a **real** failure — `DROP TABLE`, `PRAGMA query_only=ON` (to fail
a write after a prior real read in the same handler succeeds), a real
5000-char QR payload that genuinely exceeds QR capacity, a regular file
blocking `MkdirAll` — never a mock or stub repo. Two sites are
deliberately left untested and documented in-code as unreachable through
the handler as coded (see "Independent review").

Once this and its 3 siblings are in, `internal/pages` has zero remaining
`http.Error(w, err.Error(), ...)` call sites — worth a follow-up repo-wide
grep to confirm and consider narrowing `guard-i18n.sh`'s `http.Error`
exemption, per #924/#893's own note.

## Independent review

Opus, fresh context, isolated git worktree (complexity:medium →
Sonnet-builds/Opus-reviews per the model-routing rubric). Verdict:
**safe to merge**, one should-fix (fixed), two nits (one left as
documented, the LAN-discovery key nuance; one fixed as a low-cost
improvement).

**Should-fix, fixed**: the first draft added two brand-new locale keys —
`pairing.error.server` (singular) and `pluginsettings.error.server` —
each duplicating an already-existing key with a byte-identical value in
all four locales: `pairings.error.server` (plural, from ut-docs#893) and
the `plugins.error.*` namespace (4 sibling error keys already live there).
Worse than cosmetic: two of `pairing_api.go`'s sites (`ListPending`,
`discovery.TillID`) are the exact same repo calls `pending_pairings.go`
already surfaces under the existing plural key — so the new key split one
logical failure class across two names. This directly violated increment
1's own stated rule for this series ("existing keys reused where the call
site is provably the same failure an existing sibling code path already
surfaces with that key"), which the diff *did* correctly apply to
`sync_api.go` (6 of 7 sites reuse `sync.error.server`) but not here — an
inconsistency within the same PR. Fixed: `pairing_api.go`'s 7 sites now
use `pairings.error.server` (no new locale entries needed);
`plugin_settings_page.go`'s 7 sites now use `plugins.error.server` (one
new key, sharing the same generic string as its siblings). Both
now-orphaned keys removed/renamed in all 4 locale files.

**Nit, left as documented**: `sync.error.no_lan_address`'s copy describes
`lanIPv4()`'s "no non-loopback address" failure accurately, but the same
call site's other error return (`cannot list network interfaces: %w`, a
genuine syscall failure) now also surfaces under the same
network-not-connected message. Slightly imprecise, still a real
improvement over the raw leak, and the actual error is logged
server-side. Not fixed — splitting it would add a distinction with no
operator-actionable difference (both cases resolve the same way: check
the network).

**Independently re-verified, not taken on trust** (7 of the 19 tests
spot-checked in an isolated worktree, covering all three files and all
four forced-failure techniques):

| Test | Site | Technique | Result |
|---|---|---|---|
| `TestPairRequest_CreateFailureIsLocalized` | `pairing_api.go:155` | `DROP TABLE pending_pairings` | fail-on-revert, pass-on-restore |
| `TestApprovePairRequest_ApproveWriteFailureIsLocalized` | `pairing_api.go:230` | `PRAGMA query_only=ON` after a real prior read | fail-on-revert, pass-on-restore |
| `TestPluginSettingsPage_GET_ListFailureIsLocalized` | `plugin_settings_page.go:234` | `DROP TABLE plugin_settings` | fail-on-revert, pass-on-restore |
| `TestPluginSettingsAPI_POST_PlainWriteFailureIsLocalized` | `plugin_settings_page.go:350` | `PRAGMA query_only=ON` | fail-on-revert, pass-on-restore |
| `TestSyncEnrollToken_QRTooLargeFailureIsLocalized` | `sync_api.go:269` | real oversized QR payload | fail-on-revert, pass-on-restore |
| `TestSyncSnapshot_BackupDirFailureIsLocalized` | `sync_api.go:391` | regular file blocking `MkdirAll` | fail-on-revert, pass-on-restore |
| `TestSyncPromote_ClearIdentityFailureIsLocalized` | `sync_api.go:487` | `PRAGMA query_only=ON` | fail-on-revert, pass-on-restore |

Every revert produced the exact raw string the corresponding test's
negative assertion is written to catch; every restore returned to green.
The `query_only` tests are genuinely site-isolating (not whole-handler
failures) — e.g. the approve-write test only reaches `Approve` because
the preceding `GetByID` SELECT really succeeds first. Status-code
preservation checked mechanically across all 21 hunks: 20 ×
`http.StatusInternalServerError`, 1 × `http.StatusConflict`, no drift.

- The two deliberately-untested sites' unreachability claims both
  confirmed by reading the actual code paths:
  `plugin_settings_page.go`'s `parseTaxOverrides` fallback is
  unreachable because its only non-nil error return is the
  `httpStatusError` value the branch above already handles (the type
  assertion always succeeds); `sync_api.go`'s `lanIPv4()` failure needs a
  machine with no up non-loopback IPv4 interface, unforceable from a Go
  unit test without substituting `net.Interfaces()` (ruled out — not a
  "real" failure).
- Shared-generic-key claim confirmed on the merits: all 14
  `pairing_api.go`/`plugin_settings_page.go` sites are plain
  infrastructure failures on a repo call with no operator-recognisable
  distinguishing context — the genuinely distinguishable outcomes in
  these handlers (404 not-found, 409 already-resolved) were already
  separately keyed before this diff and are untouched by it.
- Locale key parity: 1603 keys in each of en/ar/fa/tr (post-fix count),
  zero missing/extra/duplicate, all four files valid JSON. No
  `%s`/`%d`/`%v` in any of the 3 keys touched, so no format-token parity
  risk. Translations checked for naturalness in ar/fa/tr — real,
  idiomatic text, no English left untranslated, no mojibake.
- `web/help/img/manifest.json`'s surface hash: verified by running the
  real generator twice on the branch (reproduced the committed hash
  exactly) and once on a clean-`main` control (reproduced the
  pre-this-PR hash exactly) — the bump is genuine and correctly scoped.
  Two PNGs (`en/invoices.png`, `fa/translations.png`) churn on every
  regeneration **including on clean `main` with none of this diff's code
  present** — pre-existing screenshot-run nondeterminism in this
  environment, unrelated to this diff, and CI-invisible since the guard
  never hashes PNG bytes. Reverted both to keep the diff scoped.
- Recurring bug classes: clean. The three touched files contain no
  `os.MkdirAll`/`os.Create`/`os.WriteFile`/`OpenFile` calls at all, and no
  cwd-relative path where `paths.Data(...)` belongs.
- No secrets, no real client/shop names. Test fixtures use only synthetic
  names and RFC1918 addresses.
- No manual/help-topic update owed: the diff touches zero files under
  `web/ui/` (only `.go` sources, tests, locale JSON, and the docs-shots
  manifest) — no success-path template changed on any route. Six of
  seven affected routes are under `/api/` (denylisted from route
  coverage); the one page route (`GET /plugins/{id}/settings`) is already
  claimed by the `plugins` help topic.

## Verified beyond automated tests

- `gofmt -l .`, `go build ./...`, `go vet ./internal/pages/...` — clean,
  both before and after the review-driven key-consolidation fix.
- Full test suite matching CI's actual invocation —
  `go test $(go list ./... | grep -v '/internal/plugins$')` and
  `go test -timeout 20m ./internal/plugins` — all green, post-fix.
- All 16 CI-blocking guards from `.github/workflows/ci.yml`'s `build` job
  — green, including a real `make docs-shots` run (Playwright/Chromium,
  92 screenshots) for the docs-shots guard, both pre- and post-fix.
- TDD claim re-verified independently three times across the pipeline:
  once by the Dev subagent's own revert/restore pass on all 19 tests,
  once by this orchestrator (spot-check on
  `TestPairRequest_CreateFailureIsLocalized`), once by the Reviewer step
  in an isolated worktree (7 tests, all three files, all four forced-
  failure techniques — table above).
- Merged onto current `main` (which had advanced to include ut-docs#945
  and #950 mid-cycle) before opening the PR; full gate re-run clean on
  the merged tree, including a genuine `web/help/img/manifest.json`
  surface-hash regeneration (not a manual pick of either side).

## Explicitly deferred (not fixed here, tracked separately)

1. **Audit log raw error text / `LogAndLocalizedError`'s use of stdlib
   `log.Printf`** and **API envelope** (`{"data": …, "error": null}` vs
   these `text/plain` `http.Error` bodies) — both pre-existing across
   every #316/#893/#924 call site, neither a regression from this diff.
   Already tracked by a follow-up card opened during increment 1's review.
2. **`ut-plugin-language-{de,es}` packs** — this session filed and merged
   the follow-up translations for this increment's new/changed keys
   directly (`sync.error.no_lan_address`, plus the 12 keys increment 3
   added) after finding `main`'s `lang-pack-drift` check red mid-cycle —
   see the merged PRs in each pack repo. `lang-pack-drift` is
   advisory-only on this core PR (only blocking on push to `main`), so
   this doesn't block this PR either way.
3. **`sync.error.no_lan_address`'s minor imprecision** (documented above)
   — not fixed, no operator-actionable distinction to make.

## Safe-to-merge verdict

Safe to merge. The one should-fix from independent review (duplicate
locale keys) fixed and re-verified with a full gate re-run; all
CI-blocking guards green; full test suite green (matching CI's real
invocation); TDD claim re-verified independently three times across the
pipeline, including per-site revert/restore on 7 of the 19 new tests
covering every forced-failure technique used.
