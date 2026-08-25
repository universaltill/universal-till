# Review: I18n.T guaranteed-English fallback terminal entry (ut-docs#995)

## What shipped

`internal/config/i18n.go`'s `I18n.T` resolved a key through the chain
`locale → baseLang(locale) → i.fallback → baseLang(i.fallback)`, with no
guaranteed-complete terminal entry. Found during the independent review of
ut-docs#861 (shop-level live-switchable default locale): once a shop's
`store.locale` is itself set to a non-English locale — directly reachable
from Settings' Language card since #861, not just a `UT_DEFAULT_LOCALE`
install-time edge case — a key missing from that fallback locale's own
translation source had nothing left to try, and the raw key rendered
instead of English text. Shipped locales can't drift (`guard-i18n.sh`
enforces key-set parity against `en.json`), but an overlay-provided
language-pack plugin (`ut-plugin-language-de`, `ut-plugin-language-es`)
legitimately can — `lang-pack-drift` CI's own advisory-then-blocking design
exists specifically to tolerate temporary drift there.

Fix: append a new `baseLocale = "en"` constant as a guaranteed terminal
entry to `T`'s resolution slice. Because the loop returns on first hit,
appending an entry at the end can only change outcomes that previously
fell through the entire chain and returned the raw key — no other lookup
result can change.

`Entries()` (the translation editor's Reference column) was considered —
the original issue flagged it as "worth checking" — but a first attempt
revealed a separate, deeper gap: `Entries()` only ever discovers candidate
keys from `{fallback, base(fallback), locale, base(locale)}`, so a key that
exists *only* in `en.json` and not in the fallback locale's own bundle is
never discovered at all, regardless of the Reference-lookup fallback. That
is a real gap but distinct from this card's scope (T's resolution chain,
not Entries' key-discovery) — deferred, filed as a follow-up (see below).

## Independent review

Opus, fresh-context, isolated worktree (`isolation: "worktree"`).

**Verdict: PASS, no blocking findings.**

**Findings, triaged:**

- **Fixed** — bare `"en"` magic literal in the resolution slice. Added a
  package-local `const baseLocale = "en"` (the codebase already names this
  concept at `internal/manual.FallbackLocale`, but importing `internal/manual`
  from `internal/config` would be the wrong dependency direction, so a
  local const instead).
- **Not fixed here — filed as follow-up** — `Entries()`'s key-discovery
  gap (above): it can now (as before) miss listing a key that `T` is able
  to resolve via the new English terminal fallback, because `Entries()`
  never scans `en`/`baseLocale` for candidate keys when the shop's fallback
  is a different locale. Filed as ut-docs#997 so the translation editor's
  key inventory stays honest for a non-English `store.locale`, matching
  `architecture/translation-editor.md`'s own documented union-of-sources
  spec.
- **Not fixed — accepted as-is (soft nit)** — the new test doesn't
  separately exercise a request locale different from the fallback, or a
  full BCP-47 tag on the fallback side; both are already covered
  transitively by the rest of the suite (`TestT_FallsBackFromRegionTagToBaseLanguage`,
  `TestNewI18nFS_MissingFallbackLocaleIsEmptyNotError`). Not worth a second
  review round for test-shape polish on a PASS with no blocking findings.
- **Not fixed — pre-existing, untouched by this diff** — `newTestI18n`'s
  dead `dir := t.TempDir(); _ = dir` in `i18n_overrides_test.go`, noted by
  the reviewer only because the file was touched. Out of scope for this
  card.

## Verified beyond automated tests

- **TDD re-verification (revert → fail → restore → pass)**, done
  independently by the reviewer in its own isolated worktree, not just
  taken on the implementer's word: removed only the trailing `baseLocale`
  entry, re-ran the regression test, confirmed it failed with exactly the
  raw-key-back symptom (`T(de, only.en) = "only.en", want the English
  fallback`) while the test's *other* assertion (the already-working `de`
  translation) still passed — proving the test discriminates the specific
  bug, not a trivial always-fails case. Restored the fix, confirmed green,
  confirmed the file was byte-identical to the reviewed commit afterward.
- Ran the **full repo suite** (`go test ./...`), not just
  `internal/config`, with the fix reverted — exactly one test failed
  (the new one), which is direct evidence for the "no change to existing
  shipped-locale behavior" acceptance criterion.
- `-race` on `internal/config`: clean (the added lookup lives inside `T`'s
  existing `i.mu.RLock()`/deferred-unlock section, touches only the three
  maps already covered there, and `SetOverlays`/`SetShopOverrides` already
  swap those maps wholesale under the write lock — no torn reads).
- Grepped the diff for the two recurring bug classes this pipeline keeps
  finding (a file-write handler missing `os.MkdirAll`, a cwd-relative path
  where `paths.Data(...)` belongs): zero hits, pure in-memory map logic.
- Confirmed plugin trust ordering is unchanged (ADR-0006): the terminal
  entry still checks `messages["en"]` before `overlays["en"]`, so a
  language-pack plugin still cannot hijack a core English string, only
  fill a key core defines nowhere.
- Traced the real trigger path: `internal/pages/init.go` constructs the
  translator with `state.Locale` as fallback, so a shop with a persisted
  non-English `store.locale` boots with a non-English `I18n.fallback` —
  and `httpx.SetDefaultLocale` (the live #861 Settings switch) deliberately
  never updates `I18n.fallback` afterward, so the new terminal entry is the
  only real floor against the raw-key symptom.
- No real client/shop name, no secret-shaped literal in the diff (test
  fixtures are generic: `de`, `basket.total`, `only.en`).
- No docs-repo reference documents `T`'s fallback chain, and this is an
  internal correctness fix with no UI or step change, so no `web/help/`
  manual topic update was owed.

## Safe-to-merge verdict

Yes. Full gate green on the final diff: `gofmt`, `go build ./...`,
`go vet ./...`, `go test ./...` (full repo, plus `-race` on
`internal/config`), and every CI-blocking guard in `ci.yml`'s `build` job
(`guard-data-access.sh`, `guard-kiosk-engine.sh`, `guard-plugin-menu-read.sh`,
`guard-i18n.sh`, `guard-compliance-claims.sh`, `guard-docs-shots.sh`,
`guard-help-topics.sh`, `guard-webkit-version.sh`, `guard-kiosk-launch-flags.sh`,
`guard-android-status-address.sh`, `guard-android-i18n.sh`, `guard-emoji-font.sh`,
`guard-htmx-loaded.sh`, `guard-autofill-suppression.sh`, `check-brand-assets.sh`,
`guard-makefile-version.sh`).

## Explicitly deferred

- `Entries()`'s key-discovery gap (translation editor Reference column) —
  filed as ut-docs#997.
- Test-shape nits (explicit non-fallback-locale / full-BCP-47-tag cases) —
  already covered transitively elsewhere in the suite; not worth a second
  review round on a PASS with zero blocking findings.
