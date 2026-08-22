# Journal detail: surface card-present reconciliation fields (ut-docs#792)

**Card:** ut-docs#792 · **Complexity:** easy · **Build:** Sonnet (inline) ·
**Review:** fresh-context Sonnet subagent

## Requirement

`GetSaleDetail`/`GetSaleDetailByID` already persist and return masked PAN,
auth code, terminal ID and trace ID for a card-present payment (ut-docs#543,
merged in universal-till#387), but the only place any of it was ever shown
was the one-time printed/on-screen receipt at the moment of tender. A shop
owner reconciling a card payment days later via the Journal had no way to
see it. This card adds those fields to `journal_detail.html`'s payment row.

## Change

- `web/ui/pages/journal_detail.html`: payment row now shows masked PAN +
  auth code (mirroring `receipt.html`'s existing pattern exactly, including
  reusing its `receipt.auth_code` locale key rather than duplicating it),
  falling back to the plain `Reference` display when no card-present data
  is present. Terminal ID / trace ID render on a second line when either is
  set.
- Two new locale keys, `journal.detail.terminal_id` / `journal.detail.trace_id`,
  added with real (non-placeholder) translations to all four shipped
  locales (en/ar/fa/tr).
- `web/help/en/reports.md`: new "Reconciling a card payment (receipt
  detail)" section documenting the behaviour — factual capability
  description only, no compliance-certification-outcome wording.
- `web/help/img/**` + `manifest.json`: regenerated via `make docs-shots`
  (required after both the template and the manual-content edit;
  `guard-docs-shots.sh` tracks the app-surface hash and the manual's own
  markdown separately, and failed until both were fresh).
- `internal/pages/journal_page_test.go`: new
  `TestJournalDetail_ShowsCardPresentReconciliationFields`, covering:
  - all four card-present fields set → all four render;
  - a plain cash sale (no card-present fields) → none of it renders, old
    behaviour unchanged;
  - a Reference-only payment (no MaskedPAN — today's SumUp/QR-pay/demo
    shape) → Reference still renders, confirming the fallback branch;
  - MaskedPAN set *together with* a raw Reference → MaskedPAN wins, the
    raw Reference does not also leak onto the page;
  - TerminalID set alone (no TraceID) → renders standalone, no dangling
    middle-dot separator.

No new SQL, no schema change (the persistence/migration and its own
round-trip test already shipped with #543 — see
`internal/data/pos_repo_card_present_test.go`). No new architecture, no ADR
needed — this is UI/reconciliation completeness on already-persisted data,
matching the issue's own "no business decision needed" note.

## Independent review (fresh-context Sonnet subagent)

Verdict: **APPROVE WITH NITS**. No blocker/major findings. Confirmed:
template logic correct for all field-combination cases traced; i18n keys
present with real translations in all four locales and `guard-i18n.sh`
green; no raw/full PAN ever threaded through the change (only the
pre-masked `MaskedPAN` the data layer already produces is rendered); manual
copy accurate to the code and `guard-compliance-claims.sh` green;
`guard-help-topics.sh`/`guard-docs-shots.sh` green with regenerated
screenshots.

One real gap flagged: the original test only covered "all four fields set"
and "none set", missing the Reference-only fallback case, the
MaskedPAN-suppresses-Reference case, and a TerminalID-alone case. Fixed
before commit — see the four listed cases above; all now covered and
passing. (Pre-existing, out of scope, not touched by this diff:
`SaleDetailPayment.Amount`/`ChangeGiven` are raw `int64` rather than
`money.Money` — noted by the reviewer, not introduced or worsened here.)

## Verification beyond automated tests

- `go build ./...`, `go test ./...` (full suite, all packages green).
- `gofmt -l` clean on all touched Go files.
- Every CI-blocking guard in `.github/workflows/ci.yml`'s `build` job run
  locally and green: `guard-data-access`, `guard-kiosk-engine`,
  `guard-plugin-menu-read`, `guard-i18n`, `guard-compliance-claims`,
  `guard-docs-shots` (after `make docs-shots`), `guard-help-topics`,
  `guard-webkit-version`, `guard-kiosk-launch-flags`,
  `guard-android-status-address`, `guard-android-i18n`, `guard-emoji-font`,
  `guard-htmx-loaded`, `guard-autofill-suppression`, `check-brand-assets`,
  `guard-makefile-version`.
- Manually traced the template's conditional branches against
  `SaleDetailPayment` (`internal/data/pos_repo.go`) for every field
  combination the reviewer called out.

## Not in scope

- Wiring a real card-present terminal integration (ut-docs#515) — this
  card only surfaces data the model already supports; no live producer
  exists yet, same non-goal the issue itself states.
- Translating the new `reports.md` manual section into ar/fa/tr — the
  manual's per-locale content already lags English generally (tracked
  separately, ut-docs#341); this card only had to keep the *UI* locale
  files (`web/locales/*.json`) in parity, which it does.
