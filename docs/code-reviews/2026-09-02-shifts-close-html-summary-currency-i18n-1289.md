# 2026-09-02 — shifts_api.go `respondCloseSuccess` currency-aware + translated

Card: ut-docs#1289 (closes ut-docs#1401 as a duplicate — same defect,
narrower scope, filed independently before it cross-referenced this one).

## What shipped

`internal/pages/shifts_api.go`'s `respondCloseSuccess` — the HTML-fragment
path of `POST /api/shifts/close` (what the close-shift form actually
renders on screen; the JSON API path was already correct) — hardcoded:

```go
msg := fmt.Sprintf("<div class='success'>Shift closed. Expected: £%.2f, Actual: £%.2f, Variance: £%.2f</div>",
    float64(data.ExpectedCash)/100, float64(data.ClosingCash)/100, float64(data.Variance)/100)
```

Two separate defects, same root cause class as #1274/#1290:

1. Hardcoded `£` symbol and `/100` scale — wrong on any non-GBP or
   0-decimal currency (IRR/IRT/IQD/AFN/JPY): a 500-toman shift showed as
   `£5.00`.
2. Untranslated English prose, never routed through `T()` — invisible to
   `guard-i18n.sh` because it's a Go-side `fmt.Sprintf`, not template
   markup.

Fix:

- `locale := httpx.ResolveLocale(w, r)`, then each figure formatted via
  `httpx.FormatMoney(minor int64, locale)` — the same currency-aware
  formatter #1274/#1290 already established for this page.
- The sentence itself now goes through two new i18n keys,
  `shifts.close_success` / `shifts.close_success_with_skim`, added to all
  four locale files (`web/locales/{en,ar,fa,tr}.json`), interpolated via
  `fmt.Sprintf(httpx.T(locale, key), …)` — the existing convention this
  file's sibling handlers already use (e.g. `data_api.go`'s
  `archives_purge_retained_until`).
- Output now passed through `html.EscapeString` (defense in depth — no
  formatted amount or shipped translation currently contains markup, but
  a plugin language pack or future locale addition could).
- New test: `TestCloseShift_HTMLSummaryIsCurrencyAwareAndTranslated`
  (`internal/pages/shifts_api_test.go`) — covers the plain-close figures,
  the skim/new-float figures, and that a non-English locale (`fa`) cookie
  actually renders translated prose, all under a 0-decimal currency
  (IRT/toman).

## Independent review

Opus subagent (`complexity:medium` routing — review stays Opus,
deliberately not the model that wrote the fix), isolated worktree.
**Verdict: SAFE TO MERGE, no blocking findings.**

Independently re-verified the TDD claim itself (not taken on trust):
reverted only `internal/pages/shifts_api.go` to `main` (test + locale
files left in place), confirmed the new test fails —

```
shifts_api_test.go:243: expected the IRT symbol in the close summary, got:
    <div class='success'>Shift closed. Expected: £50.00, Actual: £49.00, Variance: £-1.00</div>
```

— then restored the fix and confirmed green again.

Checked and confirmed correct: the skim sign (`FormatMoney(-data.Skim,
locale)` mirrors the original `float64(-data.Skim)/100` exactly, no
regression); zero-amount and negative-variance formatting degrade the
same as before (no `£0.00`/`£-1.00` regression); all four locale files
carry both new keys with the matching `%s` count/order (3 and 5
respectively) and plausible, complete translations; neither of the two
recurring bug classes this pipeline watches for (missing
`os.MkdirAll` / a cwd-relative path where `paths.Data(...)` belongs)
apply — the diff has no file writes and no path construction at all; no
`web/help/` manual update needed (no new/changed route or page, just an
existing confirmation fragment's wording — `guard-help-topics.sh`
confirms unchanged route coverage).

## Verification

| Check | Result |
|---|---|
| `gofmt -l` (2 changed .go files) | empty |
| `go build ./...` / `go vet ./...` | clean / clean |
| New test, both directly and via `-run TestCloseShift` | pass |
| `go test ./internal/pages/...` (full package) | pass, ~102s |
| `guard-i18n.sh` / `guard-data-access.sh` / `guard-help-topics.sh` / `guard-compliance-claims.sh` | pass |
| TDD red→green, independently re-verified by the reviewer | see above |

## Deferred (not fixed here, out of this card's scope)

- **Language-pack follow-up (mandatory, same cycle, not deferred to a
  card — CLAUDE.md / scrum-master's "work that has no card" rule):** the
  two new `en.json` keys need the same keys added to the external
  `ut-plugin-language-{de,es}` packs. Done in this same pipeline cycle,
  immediately after this PR merged — see those repos' own history for the
  commit. (`config.I18n.T`'s locale→baseLang→fallback→`en` chain means a
  pack that briefly lagged would have degraded to the English sentence
  with the `%s` count intact, never a `%!(EXTRA …)` corruption — but the
  follow-up isn't optional, `lang-pack-drift` is blocking on push to
  `main`.)
- **Sibling defect, same class, different function — filed as a new
  Backlog card:** `respondShiftSuccess` (12 lines above, shift *open*'s
  own HTML-fragment success message) still hardcodes untranslated English
  prose outside `T()` and unescaped output — not a security issue
  (`data.ShiftID` is `uuid.NewString()`), but the same defect class this
  card exists to fix. Out of scope for #1289 (which was filed specifically
  against `respondCloseSuccess`).
