# Code review: owner-required 403s — translated, full-layout error responses (ut-docs#1887)

**Branch:** `fix/1887-fiscal-owner-required-i18n`
**Reviewer:** independent fresh-context review (Sonnet), no visibility into Dev/Tester's reasoning. Sibling fix to ut-docs#1814 (`fiscal_device_page.go`); reviewed against that fix's own precedent and its review record (`docs/code-reviews/2026-09-09-fiscal-device-i18n-http-error-1814.md`).

## What shipped

Three call sites in `internal/pages/` answered HTTP 403 with a bare,
untranslated Go string literal instead of the codebase's established i18n
pattern:

1. `fiscal_country_change.go`'s `requireFiscalAuthorityForCountryChange` —
   `http.Error(w, "owner (admin) required to change country while a fiscal
   signing device is confirmed", http.StatusForbidden)` →
   `httpx.RenderError(w, r, http.StatusForbidden,
   "fiscaldevice.error.owner_required_country_change", nil)`. New locale key
   (more specific than the existing `owner_required` key — names the
   country-change context), added to all 4 shipped locales.
2. `settings_page.go`'s raw-key `/api/settings/upsert` handler — two
   identical `http.Error(w, "owner (admin) required", http.StatusForbidden)`
   sites (the `fiscal.KeyOverrideUntil`/`Reason`/`Actor` case and the
   `fiscal.KeySystemOfRecord`/`wireKeySigningDeviceConfigured` case) → both
   now `httpx.RenderError(..., "fiscaldevice.error.owner_required", nil)`,
   reusing the existing key from #1814 (text matches closely, no new key
   needed).
3. `fiscal_api.go`'s `createSigningOverride` (`respondFiscalError`, a
   JSON/HTML-fragment API helper, not a page route) — two
   `respondFiscalError(w, r, http.StatusForbidden, "owner (admin) approval
   required")` sites → `locale := httpx.ResolveLocale(w, r)` resolved once
   in the `if !canPerform(...)` block, then
   `respondFiscalError(w, r, http.StatusForbidden, httpx.T(locale,
   "fiscaldevice.error.owner_required"))`. Mirrors the existing
   `backup_api.go:289` / `pairing_join.go:254` pattern for locale-aware JSON
   error envelopes. `respondFiscalError`'s signature is unchanged (still
   `message string`); only what the two sites pass in changed. Its other 16
   call sites in this file are untouched (different messages, out of scope
   — tracked separately as ut-docs#893's larger sweep).

New/extended tests, all in the same WIP commit:
`fiscal_country_change_test.go` (new,
`TestRequireFiscalAuthorityForCountryChange_RefusalIsTranslatedFullLayout`),
`settings_page_test.go`
(`TestSettingsUpsert_OwnerRequiredGatesAreTranslatedFullLayout`, table test
over 3 keys, empty-value case for `KeyOverrideUntil` to route around its own
400-on-non-empty validation), and two assertions plus a Farsi (`?lang=fa`)
proof added to the existing `TestFiscalOverride_PINPaths`.

`git show --stat` on the WIP commit: exactly 10 files — the 3 handler
files, 3 test files, and 4 locale files. No drive-by changes.

## Independent verification performed

All verification below was run in an isolated worktree
(`git worktree add <tmp> <WIP-commit-sha>`, detached HEAD), never on the
shared `/home/user/universal-till` checkout — the revert step below mutates
tracked files on disk and this pipeline has previously had a reviewer's
in-place revert land in a real commit via a stop-hook race.

1. **TDD claim re-verified by hand.** Reverted only the 3 production files
   (`fiscal_api.go`, `fiscal_country_change.go`, `settings_page.go`) to
   `HEAD~1` (test files left at `HEAD`), confirmed a clean build
   (`go build ./internal/pages/...` — no unused-import fallout from
   dropping `fiscal_country_change.go`'s new `httpx` import), then re-ran
   the three target tests. All three failed with real assertion errors, not
   compile errors:
   ```
   fiscal_country_change_test.go:33: error response has no nav rail (bare body, dead end on a pinned kiosk):
       owner (admin) required to change country while a fiscal signing device is confirmed
   fiscal_gate_test.go:379: expected the translated owner_required message "Owner (admin) required", got: {"data":null,"error":"owner (admin) approval required"}
   settings_page_test.go:2578: error response has no nav rail (bare body, dead end on a pinned kiosk):
       owner (admin) required
   ```
   (all 3 `TestSettingsUpsert_...` subtests failed identically). Restored
   the fix (`git checkout HEAD -- <the 3 files>`); all three tests (and
   their subtests) passed again, working tree clean. The TDD claim holds.
2. **Full gate**, this worktree:
   - `gofmt -l .` — no output.
   - `go build ./...` — clean.
   - `go vet ./...` — clean.
   - `golangci-lint run ./internal/pages/...` — 0 issues.
   - `go test ./internal/pages/...` — all green (188s).
   - `go test ./...` (full suite, every package) — all green, no failures.
   - `bash scripts/ci/guard-i18n.sh` — pass (1550 template keys resolve,
     all locales match `en.json`, no hardcoded Go-side strings).
   - `bash scripts/ci/guard-page-http-error.sh` — pass: zero bare
     `http.Error`/`LocalizedError`/`LogAndLocalizedError` remain in a
     page-route handler.
   - `bash scripts/ci/guard-compliance-claims.sh` — pass (301 files
     scanned).
   - `bash scripts/ci/guard-help-topics.sh` — pass (run as part of the
     manual-staleness check below).
3. **`httpx.ResolveLocale` called where it wasn't before — checked for a
   side-effect risk.** It can set the `ut_lang` cookie (when `?lang=` is on
   the query string) — the review brief specifically asked whether calling
   it in a new spot is safe. Read `RenderError`'s own implementation: it
   already calls `ResolveLocale` internally on every page-route error path,
   so sites 1 and 2 add no new call at all. For site 3
   (`fiscal_api.go`), grepped the whole handler: `ResolveLocale` is called
   **exactly once**, only inside the `if !canPerform(...)` block, so there
   is no double-call within this handler. Across the codebase more broadly
   (`internal/ui/buttons.go`, `internal/pages/catalog/handlers.go`,
   `backup_api.go`, `pairing_join.go`, etc.) it is already called
   per-handler in many places without issue — setting the same cookie value
   twice in one response is harmless (last `Set-Cookie` for a given name
   simply repeats the same value). No behavioral risk.
4. **Locale key correctness.** Grepped `internal/pages/` for any remaining
   literal `"owner (admin)"` string — zero hits outside test-file comments
   documenting the historical bug, confirming all 3 in-scope sites were
   actually migrated (no site that was supposed to change but didn't).
   Confirmed the new key name
   (`fiscaldevice.error.owner_required_country_change`) matches exactly
   between `fiscal_country_change.go`, its test, and all 4 locale files —
   no typo.
5. **Translation sanity** (`ar`, `fa`, `tr`): read the actual strings and
   compared against the existing `owner_required` key's own established
   phrasing in the same files (ar "المالك (المدير العام)", fa "دسترسی مالک
   (ادمین)", tr "Mağaza sahibi (yönetici)") — the new
   `owner_required_country_change` strings reuse that exact vocabulary and
   extend it with the country-change clause, not a re-translation from
   scratch or machine-garbage. Plausible and internally consistent; not
   claiming native-fluency sign-off.
6. **Recurring bug classes**: `grep -n "MkdirAll\|os.WriteFile\|os.Create\|paths\."` on
   the 3 changed production files — zero hits. No file writes in this
   diff at all; both classes are N/A, confirmed rather than assumed.
7. **No client/shop name, no secret-shaped literal**: grepped the full
   diff for known-client markers and secret/token/password/key patterns —
   none found.
8. **Manual/help topic staleness**: `guard-help-topics.sh` passes; grepped
   `web/help/` for the old and new error text ("owner (admin) required",
   "country while a fiscal") — zero hits in any topic. This is an
   internal error-string fix on an existing admin-only permission gate,
   not a new page or an operator-visible workflow change — no manual
   update needed. Judgment checked, not assumed.
9. **UX/visual-surface checklist**: agree with skipping it. This diff
   changes no template, no CSS, no layout — it swaps a raw-string call for
   an existing, already-reviewed rendering path (`httpx.RenderError`,
   already covers RTL/nav-rail/full-layout per #1814) and an existing
   JSON-envelope helper. No new UI surface is introduced.
10. **Cross-repo lang-pack-drift check** (advisory on this PR, blocking on
    push to `main` per `universal-till/CLAUDE.md`): cloned
    `ut-plugin-language-de` and `ut-plugin-language-es` at their own
    `main` tips. `fiscaldevice.error.owner_required` (the reused key) is
    already present in both packs. The **new** key,
    `fiscaldevice.error.owner_required_country_change`, is **not** present
    in either — confirmed by direct grep, zero hits in both files. Not a
    defect in this diff (the PR only touches `en.json` + in-repo
    `ar`/`fa`/`tr`, so this PR's own CI stays green), but merging this to
    `main` as-is will turn `main` red on `lang-pack-drift` until a
    follow-up PR lands the one new key in both pack repos. Flagging for
    whoever merges, same as the #1814 review's own note for its two keys.

## Findings

No blocking findings.

- **Deferred (out of scope, confirmed intentionally untouched):**
  `respondFiscalError`'s other 16 call sites in `fiscal_api.go` (different
  messages — "invalid JSON", "settings store unavailable", "too many
  attempts", etc.) — tracked under ut-docs#893's larger sweep.
- **Deferred / cross-repo, not a defect in this diff:** the new locale key
  `fiscaldevice.error.owner_required_country_change` is missing from
  `ut-plugin-language-de` and `ut-plugin-language-es` on their own `main`
  branches (see point 10 above). Needs a small follow-up PR in each pack
  repo before or immediately after this merges, or `main`'s
  `lang-pack-drift` check goes red. Not this card's scope to fix (separate
  repos, this worktree has no push access to them); noted for the
  orchestrator/merger.

## Verdict

**Safe to merge.** The change is exactly what it claims: 3 call sites
migrated to the codebase's established i18n error-rendering pattern (2
reusing an existing key, 1 adding a correctly-named new one), a genuinely
independent, re-verified TDD regression test per site, a clean full gate
(build/vet/lint/full test suite/all 3 required guards), and no behavior,
permission-gate, or status-code change — text and rendering only. The one
real consequence to flag is the language-pack drift noted above, an
operational/merge-sequencing note, not a defect in this diff.
