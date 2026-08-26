# i18n: translation editor's Entries() never discovers a key that exists only in en.json (ut-docs#997)

**Card:** universaltill/ut-docs#997 — `I18n.Entries(locale)`
(`internal/config/i18n.go`) built its candidate key set by scanning only
`{i.fallback, baseLang(i.fallback), locale, baseLang(locale)}`. If a shop's
`store.locale` (ut-docs#861) is a non-English fallback whose own bundle
(typically an overlay-only language-pack plugin) is missing a key that
`en.json` has, `Entries()` never listed that key at all — not blank, not
untranslated, simply absent from the union. This diverged from the shipped
spec (`docs/architecture/translation-editor.md`: "union of: base en.json
keys, active plugins' overlay keys, and existing DB overrides") and from
`T()`'s own resolution chain, which already unconditionally falls back to
`baseLocale` ("en") per ut-docs#995.

**Complexity:** easy. Dev inline (Sonnet), review at Sonnet fresh-context
subagent per this repo's model-routing note, isolated worktree.

## What shipped

1. `Entries()`'s key-collection loop now also scans `baseLocale`
   unconditionally, mirroring `T()`'s terminal fallback — so the editor's
   key inventory always contains at least every key `en.json` defines,
   regardless of `store.locale`.
2. `Reference` (the fallback-locale text shown beside the edit box) now
   falls back to `baseLocale` when the shop's own fallback locale doesn't
   have the key either — otherwise the newly-discovered row would show an
   empty reference, leaving the translator nothing to translate *from*.
3. New test `TestEntries_MissingKeyInNonEnglishFallbackStillListed`,
   mirroring the existing `TestT_MissingKeyInNonEnglishFallbackFallsBackToEnglish`
   but asserting on `Entries()`'s output: an overlay-only `de` fallback
   missing `only.en` (present in `en` only) now gets a row with
   `Source == ""` (untranslated), `Value == ""`, and
   `Reference == "English only"`.

## Independent review

Verdict: **safe to merge, no fix needed.**

- **TDD claim independently re-verified**, in an isolated worktree: reverted
  just the `Entries` function body to `main`'s version (kept the new test),
  ran it — real assertion failure
  (`i18n_overrides_test.go:97: Entries(de) missing "only.en" entirely, want
  it listed as untranslated`), not a compile error. Restored the fix,
  confirmed byte-for-byte restore and the full package green again.
- **English-fallback shops (the common case) are provably unaffected**: a
  throwaway test with `fallback: "en"` showed `Entries("fr")` returns
  exactly the same 2 entries as before, no duplicates — the `baseLocale`
  addition is a no-op whenever `i.fallback == baseLocale`, since the loop
  already scanned `i.fallback` in that case, and `keys` is a
  `map[string]bool` so re-scanning the same locale is idempotent by
  construction.
- **`Reference` vs. `Value` asymmetry is intentional, not a bug**: `Value`/
  `Source` (via `lookupWithSourceLocked`) deliberately do *not* fall back
  beyond the locale being edited — "the editor shows what this locale
  itself defines" — while `Reference` mirrors `T()`'s full fallback chain,
  because its only job is showing the translator what English says. Correct
  editor semantics.
- **Only other caller of `Entries()`** (`grep -rn "\.Entries(" --include=*.go`)
  is `internal/pages/translations_page.go:51`, unchanged by this diff; all
  10 of its existing tests still pass.
- **One non-blocking nit, pre-existing, not introduced here**: the
  `Reference` fallback uses `""` as its "not found" sentinel
  (`lookupWithSourceLocked` already returns `("", "")` for "not found"
  everywhere in this file), so a fallback-locale bundle with a
  *legitimately* empty string for a key would incorrectly still show the
  English reference instead of the empty value. Inherited ambiguity in the
  existing lookup contract, out of scope for this fix, not worth a separate
  touch.
- No SQL (pure in-memory map logic — not a repository-pattern concern
  regardless), no money, no new user-facing string (this only changes which
  rows populate an existing table; `translations_page.go` and its template
  are untouched), no `web/help` manual impact (no route/page/UI copy
  changed), no secrets, no real client/shop names.

## Verification beyond automated tests

- `go build ./...`, `go vet ./internal/config/...`, `gofmt -l .` — clean.
- `go test ./internal/config/... -race -v` — all 16 tests pass, including
  the new one.
- Full `go test ./...` (non-`-race`, whole repo) — all packages `ok`. The
  `internal/pages` package's own `-race` run hit the pre-existing,
  already-tracked 10-minute timeout (ut-docs#1119) — unrelated migration/
  goroutine dump, not touching i18n code, not caused by this diff.
- `go test ./internal/pages/... -run TestTranslations -v` — all 7
  translation-page tests (the only real caller of `Entries()`) pass.
- `bash scripts/ci/guard-data-access.sh`, `bash scripts/ci/guard-i18n.sh` —
  both pass.
- No visual surface touched (backend-only logic feeding an existing page) —
  no screenshot/driven-run needed per the tester skill's own scoping.

## Deferred / accepted, not fixed here

- The pre-existing `""`-as-sentinel ambiguity in `Reference`'s fallback
  (see above) — real but inherited, disproportionate to this task's scope.

## Safe-to-merge verdict

**Yes.** Build/vet/fmt clean, full test suite green, relevant CI guards
pass, TDD claim independently re-verified against the pre-fix code, no
i18n/UX/manual impact, no secrets or real client data.
