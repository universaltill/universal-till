# Review: GDPR erasure keeps the issued-invoice buyer snapshot (ut-docs#2965)

- **Date**: 2026-09-29
- **Branch**: `feat/2965-erasure-keeps-invoices`
- **Card**: ut-docs#2965 (Portugal track C10), `lane:local-mahshid76`
- **Author model**: Claude Opus 5.5. **Reviewer**: Claude Fable 5.1
  (independent subagent; `complexity:medium`).
- **Companion**: ut-docs `docs/2965-erasure-gdpr-basis`
  (`reference/portugal-compliance.md`).

## What shipped

The BA found that the card's core ask already existed. `invoices` /
`invoices_archive` hold a text snapshot of the buyer (`customer_name`,
`customer_address`, `customer_vat_no`), and `POSRepo.EraseCustomer` never
touches it. This branch:
- **pins** that with `TestEraseCustomer_LeavesInvoiceBuyerSnapshotUntouched`
  (live and archived invoices, via a real `ResetTransactionHistory`);
- documents it on `EraseCustomer`;
- tells the manager. A new key, `settings.data.gdpr_invoices_kept`, is shown
  under the GDPR card on Settings → Data and appended to the erase PIN
  prompt (en, ar, fa, tr). Help item 16 in `web/help/{en,ar,fa,tr,de}` is
  updated too.

## Verification

- **TDD**: the elevation-text assertion failed first with the real error
  (the summary had no such sentence), then passed after the change.
- **Mutation check on the pin test** (WSL clone): making `EraseCustomer`
  blank `customer_vat_no` on `invoices`, and then on `invoices_archive`,
  failed the test each time with "buyer snapshot changed by erasure". It
  passed again once restored. (A first mutation keyed on
  `sales.customer_id` was a no-op, because the unlink runs first. That was
  caught and redone.)
- gofmt clean, `go vet` clean, the targeted tests pass, and
  `guard-{i18n,help-topics,help-drift,compliance-claims,data-access,core-neutral}`
  pass.
- **Full gate (Linux, fresh clone of `main` + this diff):** `go build ./...`
  and `go vet ./...` clean, and `go test -timeout 20m ./...` exit 0 with 76
  packages ok. It ran before the review's wording fix. After the fix, gofmt,
  vet, the four text guards, and the `internal/data` + `internal/pages`
  erase/help/i18n tests were rerun: green.
- **Looked at** (headless Chrome, a throwaway data dir, `UT_AUTH=off`):
  - Settings → Data → Erase a customer in **en** at 1280×900: the new
    muted paragraph sits under the GDPR help, with nothing overlapping.
  - **fa** (RTL) at 1280×900: right-aligned, wraps cleanly.
  - **tr** at 800×1280: wraps with nothing clipped.
- **Not looked at**: dark theme (it uses the same `muted` class as the
  neighbouring paragraph), and the PIN-prompt dialog visually (its text is
  asserted by the handler test).
- Playwright e2e: green in the PR's CI (`playwright`, `locale-render-audit`).
- **docs-shots:** first CI run failed `guard-docs-shots`, because help item
  16 and `/settings` changed. `make docs-shots` was run locally (WSL, Node
  20, Playwright Chromium 149): 120/120 passed.
  - Only `web/help/img/manifest.json` is committed (display topic hashes and
    `surface_sha256`).
  - The display screenshot is the top of `/settings`; the new paragraph is
    further down, in the Data card.
  - Old and new `en/display.png` are visually identical. The byte-level PNG
    differences across all topics are local font/Chromium noise.
  - `guard-docs-shots.sh` passes with the new manifest.

## Findings

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Major | The text "for as long as the law requires you to keep them" implied the buyer details get cleared after retention. Nothing ever clears them. | Fixed: "exactly as issued, because tax law requires you to keep them", in 4 locales + 5 help files. |
| 2 | Major | Doc cross-references pointed to §8 (IVA rates) instead of §13. | Fixed (ut-docs). |
| 3 | Minor | The doc described `EraseCustomer` as unlinking only `sales.customer_id`. | Fixed (ut-docs). |
| 4 | Minor | The doc marked the Despacho reprint row "Met" before any PT document exists. | Fixed: "Partially met". |
| 5 | Minor | Code/test comments stated Art. 17(3)(b) as fact, while the reference marks it unverified. | Fixed: "expected basis … not yet verified". EUR-Lex was unreadable again this pass. |
| 6 | Minor | Two older doc passages still described C10 as open. | Fixed (ut-docs). |
| 7 | Minor | de/es/pt packs need the new key. | Pack PRs in the same cycle, after the core merge (brand-new key, #1576). |
| 8 | Minor | The elevation test asserts English only. | Accepted: the concatenation is locale-agnostic (two full sentences joined by a space; the reviewer checked RTL). |
| 9 | Nit | Lei 58/2019 art. 28(3) → 28(3)(b), wording "requirement of lawfulness". | Fixed (ut-docs). |
| — | Out of scope | Erasure leaves PII on LAN replica tills (prune retires the row in place) and in parked-basket payloads; plugins have no erasure event. | Verified in code; filed as **ut-docs#3253** (Triage, bug). |

The pin test was judged real by the reviewer: it catches both a rewrite
and a delete of the snapshot, live and archived. Credit notes are not
covered, which is acceptable.

## Verdict

Safe to merge after the fixes above and a green full gate.

## Deferred

- ut-docs#3253 (PII on replicas / held sales / plugins).
- ut-docs#3252 (merchant DPA, Admin Review).
- ut-docs#2958 carries the PT buyer-NIF snapshot requirement.
