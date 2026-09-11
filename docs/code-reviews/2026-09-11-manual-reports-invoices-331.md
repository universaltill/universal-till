# Code review: manual content — reports/journal/shifts/invoices/refunds/audit (ut-docs#331)

**Card:** universaltill/ut-docs#331 — "Manual content: reports, journal,
shifts, invoices & refunds"
**Complexity:** medium (Dev: Sonnet inline; Review: Opus subagent, fresh
context, isolated worktree)

## What the card asked for vs. what was actually needed

BA verification (before any writing) found the card's premise partly
stale: `web/help/en/reports.md` already claimed routes `/reports,
/journal, /journal/{receipt}, /shifts, /audit` with substantial existing
content for reports, journal and shifts; `web/help/en/invoices.md`
already claimed `/invoices, /invoice/{display_no}`; refunds were already
covered in `sell.md`. Re-writing those from scratch would have duplicated
work and diverged from an already-mature manual.

Reading the actual Go handlers/templates instead of assuming the card's
list was still accurate found the real, narrower gap:

1. **`reports.md` had no content at all for "Ask your till"** — the
   AI Q&A widget that renders on `/reports` when the shop's AI plugin
   supports it (`internal/pages/ask_api.go`, `web/ui/pages/reports.html`).
2. **`reports.md` claimed `/audit` but never actually described the Audit
   page** — only passing mentions, no dedicated section.
3. **`invoices.md` was thin** — 2 steps, no error/empty states — versus
   what `internal/pages/invoice_page.go` actually does (seller-identity
   gate, idempotent issuing, automatic credit notes, date-range register,
   CSV export).

## What shipped

- `web/help/en/reports.md`: new `## Ask your till` and `## The audit log`
  sections.
- `web/help/en/invoices.md`: rewritten with `## Turning it on`,
  `## Issuing an invoice`, `## Credit notes`, `## Viewing, reprinting and
  exporting`, `## What can go wrong`.
- `scripts/ci/i18n-baseline/help-drift-baseline.json`: new/updated
  entries recording that `ar/de/fa/tr` are now structurally behind
  English on the `reports`/`invoices` topics — translation is explicitly
  out of scope for this card ("English only on this card — translation is
  a separate card", same convention as the pre-existing `catalog`/
  `till-designer` entries for ut-docs#329, tracked by ut-docs#341).
- `web/help/img/en/{reports,invoices}.png` + `manifest.json`: regenerated
  via the real `make docs-shots` Playwright harness (English locale only
  — the underlying app pages didn't change, so `ar/fa/tr` screenshots
  stayed valid and untouched).

No `.go` files, no locale JSON, no UI templates touched — content-only.

## Independent review (Opus, isolated worktree, read-only)

Ran `guard-help-topics.sh`, `guard-help-drift.sh`, `guard-docs-shots.sh`,
`guard-compliance-claims.sh`, `guard-i18n.sh` (all green), confirmed no
`.go` files in the diff, and read both regenerated PNGs to confirm real,
non-blank captures. Then cross-checked every specific factual claim in
the new prose against the actual handler/template it describes.

**Verdict: not safe to merge as first drafted** — five blocking factual
errors, all fixed in this same session before commit:

1. **Credit-note linkage was backwards.** Original text said the credit
   note "appears on the original invoice's page (linked back to it)" —
   `internal/pages/invoice_page.go` and `invoice.html` actually link the
   *credit note's* page back to the original, not the reverse. Fixed.
2. **Wrong AI settings navigation.** "Settings → Plugins → AI" doesn't
   exist — Plugins is a top-level nav item, not inside Settings, and the
   plugin's shipped name is "AI Assistant" (confirmed against
   `README.md` and the manual's own `plugins.md`). Fixed to "Plugins →
   AI Assistant → its settings page".
3. **Troubleshooting was wrong for two real cases.** The original
   "if the box doesn't appear" text implied it was always about turning
   AI on, but `internal/pages/ai_resolve.go` shows the `claude` provider
   has no ask loop at all — the box hides itself even with AI fully
   configured and working — and `reports_page.go`'s `CanAsk` gate also
   requires manager permission. Rewrote as three explicit cases.
4. **Quoted UI copy that doesn't exist.** "then **Apply**" — the audit
   filter button is actually labelled **Filter** (`audit.filter.apply`).
   Fixed, plus **Export** → **Export CSV**, **Submit** → **Issue
   invoice**, "the customer's name" → **Customer / company** (the real
   field label) throughout.
5. **Wrong placement claim, self-contradicting the same file's own
   numbered steps.** "at the top of Reports" — the widget actually
   renders after the KPI row and the manager-only Audit-trail button,
   above the report tabs. Fixed, and cross-referenced against the "How to
   use it" steps above it so the two no longer disagree.

Also fixed, non-blocking but real:
- "nothing is saved from one question to the next" read as "nothing is
  recorded" next to the surrounding privacy framing, when the question
  text is in fact written to the audit log (`ask_api.go`'s
  `ai_ask` audit entry) — reworded to say so explicitly.
- "no-sales" was listed as an audited action; no such feature/action
  exists anywhere in the codebase (only in unrelated Go comments) —
  removed from the list.
- Added "how to get there" for both new sections (the exact button
  copy/placement: **📜 Audit trail** on Reports, **🧾 Invoices** on
  Journal) — the original text just said "Open Audit"/"Open Invoices"
  with no way to find either page.
- `invoices.md`: fixed the register's "running net/tax/gross totals"
  claim (it's one **Range total (credit notes subtracted)** row, not a
  running total; on-screen credit notes show positive, only the CSV negates
  them) and the wrong redirect claim (a non-manager opening `/invoices`
  goes to the Journal, not Settings — only the seller-identity-not-configured
  case goes to Settings).
- A baseline-entry honesty nit: the new `fa`/`reports` baseline entry
  originally copy-pasted the `ar/de/tr` entries' "pre-existing ut-docs#1962
  drift folded in" reasoning, but `fa` actually matched English exactly
  before this change — that drift is new, introduced by this card alone,
  not a fold-in. Reworded to say so.

After every fix, re-ran the full guard set (`guard-help-topics.sh`,
`guard-help-drift.sh` — with the baseline's `english` signatures
re-derived from the guard's own recomputation, never hand-typed —
`guard-docs-shots.sh`, `guard-compliance-claims.sh`, `guard-i18n.sh`,
`gofmt -l .`, `go build ./...`) and all are green.

## Verified beyond automated tests

- Booted the real till server (`e2e/run-till.sh`) and fetched
  `/help/reports?lang=en` and `/help/invoices?lang=en` directly — both
  new sections render with correct HTML structure (`<h2>`, no leaked raw
  Markdown, no template errors).
- Took real headless-Chromium screenshots of both rendered manual pages
  (not just the app screens the harness captures) and looked at them:
  clean layout, correct heading hierarchy, no overlap/wrap issues, no
  visible i18n placeholder text. Checked at the manual's own two-column
  desktop layout only — this content ships English-only on this card, so
  RTL/long-translation rendering isn't yet applicable (tracked by
  ut-docs#341).
- Read both regenerated `web/help/img/en/{reports,invoices}.png`
  screenshots directly — real, correctly-rendered captures of the actual
  `/reports` and `/invoices` app pages, not blank/broken.
- `go test ./internal/manual/... ./internal/pages/...` — green (the
  manual package's own `Load()` parses every topic including both edited
  ones without error; `internal/pages` is the package the screenshotted
  routes live in).
- `go test ./scripts/ci/...`, `go vet ./...` — green.
- Killed every server process started for manual verification in the
  same session (no leftover `run-till.sh`/binary).

## Deferred (new Backlog-worthy items, not silently dropped)

- Translation of the `reports`/`invoices` topics (and the two new
  sections specifically) into ar/de/fa/tr — explicitly out of scope per
  this card's own "English only on this card" instruction; tracked by
  ut-docs#341, which already covers the whole manual's translation pass.

## Safe to merge

Yes — content-only, zero build/runtime risk (no `.go`/template/locale
changes), every CI-blocking guard for this surface green, every factual
claim cross-checked against the real handler/template it describes after
the independent review's five blocking corrections.
