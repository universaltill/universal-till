# Code review: EOD cancellations, close attribution, annotation, quantity-zero lines (ut-docs#1012)

**Branch:** `fix/1012-eod-cancellations-annotation-qty-zero`
**Author:** autonomous SDLC pipeline (Dev: Sonnet, inline; Review: Opus
subagent, independent, isolated worktree, different model from the author)
**Complexity:** relabeled `easy` → `medium` at pick-up, once scoping showed
this touches the `EODReport` struct, the aggregation SQL, `generateEOD`'s
signature and both call sites, and the printed Z-Bon footer — see the
issue's own scoping-note comment.

## What the card asked for

Three small gaps against the reference (German) day-close export:

1. Cancellations reported as their own count/net/gross, separate from
   refunds.
2. The day-close records who generated it, plus an optional annotation.
3. Quantity-zero `sale_lines` (a real unit price, not itself cancelled)
   must survive in reporting, not get silently dropped.

## What shipped

- **`internal/data/pos_repo.go`** — `EODReport` gains `CancelCount`/
  `CancelTotal` (a completed sale later voided/reversed — see "Naming
  correction" below) and `GeneratedBy`/`Annotation` (both JSON-embedded in
  `content_json`, no new `report_archive` column — same convention as
  every other `EODReport` field that isn't one of the two ut-docs#1080
  queryable columns). `dateRangeSummary` gains a second scan,
  `WHERE status = 'voided' AND date(COALESCE(voided_at, created_at),
  'localtime') BETWEEN date(?) AND date(?)` — a voided sale never matches
  the main query's `status = 'completed'` gate, so it was previously
  invisible to the day-close entirely (neither a sale, nor a refund, nor
  anything else). `COALESCE(voided_at, created_at)` is a review addition:
  every row this codebase writes stamps `voided_at`
  (`UpdateSaleStatus`'s own `CASE WHEN`), so this only guards a
  hand-inserted/legacy row with a `NULL` value, keeping it countable
  instead of silently uncountable.
- **`internal/pages/eod_api.go`**:
  - `buildEODDoc` prints a `STORNOS (voided receipts, not in revenue)`
    footer section (count + total), only when `CancelCount > 0` — same
    "omitted rather than a permanent zero line" convention as GUTSCHEINE/
    TIPS. Also prints `Erstellt von: <name>` (always, once `GeneratedBy`
    is set) and `Anmerkung: <note>` (only when `Annotation` is non-empty).
  - `generateEOD` gains an `annotation string` parameter; resolves
    `GeneratedBy` from the actor id via `AuthRepo.GetUser` (same
    users-table join precedent `internal/data`'s audit/order-status
    lookups already use), falling back to the raw actor string only when
    the lookup finds nothing.
  - New `validateEODAnnotation` (review addition, see below) rejects an
    annotation over 200 characters or containing a control character,
    checked in the `POST /api/reports/eod/run` handler **before**
    `generateEOD` runs, so a rejected value never consumes the day's
    one-shot archive attempt.
  - Both call sites updated: the unattended scheduler tick passes `""`
    (no operator, no note — same convention as its existing `actor =
    "system"`); the manual-run handler passes the request's `annotation`
    form value once validated.
- **Tests** — 4 in `internal/data/pos_repo_eod_1012_test.go` (separate
  counting + zero-day + `NULL voided_at` fallback + the quantity-zero
  invariant on `DepartmentsForDay`, plus a second lock on
  `SalesForTaxBands`, the query that actually feeds the Z-report's own
  "BY VAT RATE" section), 3 new in `internal/pages/eod_api_test.go`
  (`GeneratedBy` resolution, the seeded `"system"` actor, an unresolvable
  actor's fallback) plus 2 for the annotation validator, 2 new in
  `internal/pages/eod_test.go` (the STORNOS footer section,
  `GeneratedBy`/`Annotation` footer lines) and edits to 3 existing tests
  whose call sites/assertions the new parameter/footer layout touched.
- **Docs** — `web/help/en/reports.md` gains a "Cancellations, and who ran
  the close" section; `README.md` gets one checklist bullet.

## Independent review — two blocking issues found and fixed

An Opus subagent, different model from the Sonnet that wrote this, reviewed
the diff in its own isolated worktree (real build/vet/test/guard runs, not
just a read) and found two blockers before this could ship:

**B1 — the Cancellations row was inside `doc.Totals`, printed as a
negative, making the report's own visible arithmetic look wrong.** The
first draft put `{Label: "Cancellations (N)", Amount: "-" + money(...)}`
in the same `doc.Totals` block as Sales/Refunds/NET. A reader summing the
visible signed figures top-to-bottom would compute a different number than
NET actually shows (`Sales − Refunds`, never `− Cancellations`) — on a
document whose entire purpose is being reconcilable by eye. It also
contradicted the code's own stated rationale two blocks down ("informational,
not revenue, same as GUTSCHEINE") while placing the row somewhere GUTSCHEINE
deliberately isn't. **Fixed**: moved out of `doc.Totals` into its own
footer section (`STORNOS`, count + total), shown only when non-zero — same
shape and same "no permanent zero line" convention as GUTSCHEINE/TIPS.

**B2 — the counter measures the wrong event, and three docs said so.**
Requirement #1 says "before tender". Tracing the schema: the only writer of
a `sales` row is `POSRepo.InsertSale`, called exclusively from
`pos.CompleteSale` (the tender path) with `status = 'completed'` hardcoded.
A parked/open basket lives in `held_sales`, never in `sales`; a genuinely
abandoned basket (customer changes their mind before paying) leaves **no
row anywhere**. So `status = 'voided'` can only ever represent a sale that
*already completed* and was later reversed — a "Storno" of a receipt, which
is a real and useful figure, but the opposite of "before tender". The
original comments, `README.md` and `web/help/en/reports.md` all shipped
that wrong claim. **Fixed**: corrected every comment, the README bullet and
the help topic to describe what the counter actually measures (a completed
sale later voided/reversed), and renamed the printed section from the
generic English "Cancellations" to "STORNOS" — the reference day-close's
own term for exactly this concept, and consistent with the file's existing
fixed-vocabulary convention (`GUTSCHEINE`). **A genuine pre-tender
cancellation counter is out of scope for this card** — it would need a
persisted record of an abandoned basket, a schema change this card's own
"one item" budget doesn't cover. Filed as a Backlog follow-up (see below)
rather than silently left unrecorded.

Also fixed from the same review pass (non-blocking but cheap and real):

- **`NULL voided_at` silently dropped a row from every window's count** —
  `date(NULL, 'localtime')` is `NULL`, so `NULL BETWEEN … AND …` is `NULL`,
  excluding the row with no error. `COALESCE(voided_at, created_at)` makes
  it fail-visible (lands on *some* day) instead of fail-silent. Verified
  with a new regression test (`TestEndOfDay_VoidedSaleWithNullVoidedAtFallsBackToCreatedAt`).
- **Annotation was unvalidated free text reaching ESC/POS output
  verbatim** — `buildEODDoc`'s `"Anmerkung: " + rep.Annotation` line is
  encoded by `internal/print/escpos.go` with no control-byte stripping, so
  a control character in the annotation would be emitted as a literal
  printer command, not text. Added `validateEODAnnotation` (same
  bound/character-class convention as `internal/pos`'s
  `validateVoucherID`: ≤200 chars, no `0x00–0x1f`/`0x7f`), checked before
  `generateEOD` runs. Two new tests (`TestValidateEODAnnotation`,
  `TestPostEODRun_RejectsControlCharacterAnnotation`) cover the validator
  directly and the handler's 400 path, including confirming a rejected
  attempt doesn't consume the day's one-shot archive slot.
- Added one line of comment on the elevated-run path noting that
  `GeneratedBy` prints the *approving manager*, not the originally-blocked
  cashier — the same choice `InsertAuditElevated` already makes for the
  audit trail's primary actor, just not previously written down.

## TDD claim re-verified

The reviewer independently re-ran the revert-then-restore check for the
cancellations counter (the one genuinely new piece of query logic): removed
the new `QueryRowContext` block from `dateRangeSummary`, re-ran
`TestEndOfDay_CancellationsCountedSeparatelyFromRefunds`, confirmed a real,
specific assertion failure (`CancelCount/CancelTotal = 0/0, want 2/1000`,
not a compile error), then restored the fix and confirmed it passes again.
Also spot-checked the new tests under `TZ=Pacific/Auckland` and
`TZ=America/Los_Angeles` — both pass, confirming the
`date(..., 'localtime')` window is genuinely timezone-robust rather than
UTC-host-only (this package's own ut-docs#559 precedent for exactly this
class of bug).

## Test-quality note

The reviewer flagged `TestEndOfDay_NoCancellationsReportsZero` as
tautological — it passed unchanged with the entire feature removed, since
it only asserts Go's zero values on an otherwise-untouched path. Kept as
documentation of the "zero is a real, non-error answer" contract, but it
is not counted as coverage for the feature itself; the two-voided-sales
`CancellationsCountedSeparatelyFromRefunds` test (with a same-day/
different-day/refund/completed-sale mix, and cross-midnight `created_at`
vs `voided_at` placement) is what actually proves the query's behavior.

## Verified beyond automated tests

- `gofmt -l .` clean, `go vet ./...` clean, full `go test ./...` clean
  (no failures anywhere in the module), all four relevant CI guards green
  (`guard-data-access`, `guard-i18n`, `guard-compliance-claims`,
  `guard-help-topics`) — run personally after the reviewer's fixes were
  applied, not just before.
- Confirmed the printed footer renders correctly for: zero cancellations
  (STORNOS section absent), non-zero cancellations (count + total,
  outside `doc.Totals`), `GeneratedBy` alone, `GeneratedBy` + `Annotation`
  together, and a long annotation legitimately clipped at `print.Width`
  (42 columns) — the same clipping-not-wrapping behavior every other
  footer line in this file already has, not a new limitation this card
  introduces.
- Confirmed via `AuthRepo`'s own seed data
  (`003_system_user.sql`) that the unattended scheduler's literal
  `"system"` actor resolves to the real seeded display name `"System"`,
  not the raw lowercase string — nicer than originally assumed, and now
  covered by its own test rather than left as an accidental behavior.

## Explicitly deferred / accepted gaps

- **No on-screen annotation input.** Only the API
  (`POST /api/reports/eod/run`'s `annotation` form value) accepts one
  today. Adding a text field to `web/ui/partials/reports_tab_eod.html`
  needs a new locale key across `web/locales/{en,ar,fa,tr}.json`, and this
  cloud pipeline cycle has no route to the homelab NAS Ollama instance
  `reference/translation.md` requires for that translation — attempting a
  self-translated key would violate the same self-hosted-only rule it's
  meant to satisfy. Scoped out rather than shipped with a fabricated
  translation; a local/interactive session with NAS access can add the
  input in a follow-up.
- **A genuine pre-tender cancellation counter** (an abandoned basket, never
  tendered) is not built — see B2 above. Needs a schema change (persisting
  an abandoned/parked-then-dropped basket) that's out of this card's scope.
- `web/help/{ar,fa,tr}/reports.md` don't carry the new English section yet
  — same NAS-translation constraint as above; `guard-help-topics.sh` only
  checks topic-level presence, not content parity, so this doesn't fail CI,
  but it is a known, disclosed gap.

## Safe-to-merge verdict

Yes, after the two blocking fixes above (both applied, re-tested, and
re-reviewed against the full suite + guards). No open blockers remain.

## Follow-ups filed

- [ut-docs#1122](https://github.com/universaltill/ut-docs/issues/1122) —
  a genuine pre-tender abandoned-basket cancellation counter (schema
  change, out of this card's scope; see B2 above).
