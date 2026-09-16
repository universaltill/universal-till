# Code review — held-sale write-through stamped by the primary's clock (ut-docs#2271)

- **Date:** 2026-09-16
- **Ticket:** ut-docs#2271 (closes an accepted residual of ADR-0093 Decision 2)
- **Branch:** `fix/2271-held-sale-primary-clock-stamp`
- **Design:** ADR-0093 (`ut-docs/adr/0093-cross-till-held-sale-write-through-sync.md`),
  Decision 2 and its Amendment A F4 "clock skew" residual
- **Reviewer:** independent, different-model subagent review with real
  build/test/guard execution and genuine mutation-based TDD verification —
  not a read-only diff pass.
- **Verdict: SAFE TO MERGE.** No blocker- or money-class findings. Two
  should-fix findings (one of them genuinely load-bearing documentation,
  one mixed-version robustness gap) found and fixed in this same cycle,
  the second with its own fail-first test. The SQLite one-second
  resolution half of the same ADR residual stays explicitly deferred —
  see below.

## What shipped

`held_sales`' cross-till write-through (ADR-0093, ut-docs#1920) is
last-writer-wins on `updated_at`, guarded on the primary by
`HeldSalesRepo.UpsertIfNewer`'s `WHERE held_sales.updated_at <=
excluded.updated_at`. Until this card, the value that predicate compared
was stamped by the **replica** — `heldSaleWriteThrough` filled a blank
`h.UpdatedAt` with its own `time.Now()` before pushing. Two tills with
unsynchronised clocks therefore compared each other's wall clocks: a
genuinely newer edit taken on a slow-clocked till could lose to an older
edit from a fast-clocked one. Refused cleanly (`applied=false`), never
corrupted — but the wrong outcome, and ADR-0093 Decision 2 accepted it as
a known residual.

The fix moves the stamp to the one process that already serialises every
write to this table:

- `internal/pages/held_sale_sync_proxy.go`: `heldSaleWriteThrough` no
  longer stamps `h.UpdatedAt` at all; it sends whatever the caller passed
  (blank, for both real callers). `upsertHeldSaleOnPrimary` gained a third
  return value, `updatedAt`, read from the primary's response, and an
  applied write mirrors locally under exactly that value.
- `internal/pages/sync_held_sales.go`: `POST /api/sync/held-sales/upsert`
  stamps a blank incoming `updated_at` with **its own**
  `time.Now().UTC().Format(heldSaleTimeLayout)` before running the guard,
  and reports it back in the new `syncHeldSaleUpsertResult.UpdatedAt`
  field. A caller-supplied (non-blank) value is still honoured verbatim —
  unchanged behaviour for that case.
- Tests: the fake primary in `held_sale_sync_proxy_test.go` now mimics the
  real primary's stamping and reports `updated_at`;
  `TestHeldSaleWriteThrough_ReplicaUpsertsOnPrimaryAndMirrorsLocally`
  asserts the **wire** row is unstamped (it previously asserted the
  opposite) and that the local mirror matches the primary's reported
  stamp; new `TestSyncHeldSales_BlankUpdatedAtIsStampedByPrimaryClock`
  pins the primary side directly.

## What the independent review found and fixed

**F1 (should-fix, documentation that would reintroduce the bug).** Three
comments still described the mechanism this card removed, and one of them
guards a genuinely load-bearing line:

- `held_sale_sync_proxy.go`'s `heldSaleTimeLayout` doc still read "a
  timestamp *this till* stamps on the wire" — the replica stamping a wire
  timestamp is precisely the bug that was just closed. Rewritten to say
  the replica deliberately never formats a wire timestamp with it any
  more, and that its live use is the primary-side handler.
- The same file's header still claimed a refused till's "next
  write-through for that id **carries** a newer timestamp and applies".
  It now carries no timestamp at all; the primary stamps one. Reworded.
- `hold_api.go`'s table-move handler explains `moved.UpdatedAt = ""` as
  "cleared so *the write-through* stamps now". That is no longer where the
  stamp comes from, and the line is not cosmetic: `moved` is built from
  `heldSaleForResume`, so on a replica it carries **the primary's own
  stored `updated_at` for that row**. Sent as-is it would be honoured
  verbatim by the upsert handler and compare **equal** under
  `held_sales.updated_at <= excluded.updated_at` — the move would still
  land, but without advancing `updated_at`, leaving a concurrent older
  edit from another till free to keep applying over it. The comment now
  states that explicitly, so the line cannot be "simplified" away.

**F2 (should-fix, mixed-version robustness).** `heldSaleWriteThrough`
assigned `h.UpdatedAt = primaryUpdatedAt` unconditionally. During a
rollout a replica on this build can talk to a primary still on the
pre-#2271 build, which applies the write but answers `{"applied":true}`
with **no** `updated_at` field — decoding to `""`. Traced end to end:

- The write itself is already correct against that older primary.
  `UpsertIfNewer`'s SQL is
  `COALESCE(NULLIF(?, ''), datetime('now'))`, and the `ON CONFLICT` branch
  sets `updated_at = excluded.updated_at`, i.e. that same coalesced value —
  so a blank from the wire is stamped by the **primary's own SQLite
  clock**, and the guard is measured against the primary either way. The
  fix therefore degrades gracefully; only the *reported* value is missing.
- But the unconditional assignment would blank out a value the caller had
  supplied before mirroring it, so the local mirror would silently
  re-derive the row's timestamp from *this till's* clock — the exact thing
  this card set out to stop. Fixed by assigning only when the primary
  actually reported one, with a new fail-first test,
  `TestHeldSaleWriteThrough_PreFixPrimaryOmittingUpdatedAtStillMirrors`,
  which also pins that an applied write against such a primary still
  mirrors, still marks `primary_synced`, and never leaves the local row
  with a blank or unparseable `updated_at`.

### Checked and found clean (no change needed)

- **Callers.** `heldSaleWriteThrough` has exactly two production callers,
  both in `hold_api.go` (park/re-park at ~line 154, table move at ~line
  609), and both pass a blank `UpdatedAt` — the park builds a fresh
  `data.HeldSale` literal without the field, the move clears it
  explicitly. No caller depended on the removed pre-stamping.
  `upsertHeldSaleOnPrimary` is called only from `heldSaleWriteThrough` and
  one test.
- **The blank check cannot misfire.** `strings.TrimSpace(...) == ""` is
  applied to a field whose legitimate values are
  `"2006-01-02 15:04:05"`-shaped; no real value trims to empty. The
  assignment-back also trims a padded legitimate value, which is a strict
  improvement — this column is compared as *text*, so leading whitespace
  would previously have corrupted the ordering.
- **Both fallback branches are unaffected by no longer pre-stamping.**
  Traced `repo.Upsert`'s SQL directly rather than trusting the comment: it
  writes `updated_at = datetime('now')` on the INSERT branch *and*
  `updated_at = datetime('now')` in the `ON CONFLICT DO UPDATE` — it never
  reads `h.UpdatedAt` on either path. So the `!ok` (primary unreachable)
  and `!applied` (predicate refusal) branches behave exactly as before,
  and the refusal branch still sets `PrimarySynced = true` (Amendment A
  F11). Offline-first is in fact slightly *strengthened*: the local
  fallback now has no dependency on a wire timestamp at all.
- **The refusal invariant still holds, and more robustly.** The file
  header's promise that a refused till's next write-through eventually
  applies used to rest on that till's own clock advancing past the
  primary's stored value — which is exactly what skew could prevent
  indefinitely. It now rests on the primary's own clock advancing past its
  own stored value, which is monotonic by construction.
- **Repository pattern / money / i18n / offline-first.** No SQL added
  outside `internal/data` (`guard-data-access.sh` green). No monetary
  value handled or converted anywhere in the diff — `TotalMinor` only
  rides through as an opaque field. No user-facing string added or
  changed, so no locale-key work is owed (`guard-i18n.sh` green). No
  behaviour a shop operator sees changes, so no `web/help/` topic or
  screenshot is owed.
- **The two recurring bug classes this pipeline keeps finding.** Neither
  applies: the diff contains no file-write handler (so no missing
  `os.MkdirAll`) and no filesystem path at all (so no cwd-relative path
  where `paths.Data(...)` belongs). Confirmed by scanning the diff for
  `os.WriteFile`/`os.Create`/`os.MkdirAll`/`os.OpenFile`/`filepath.Join`/
  `ioutil` — zero hits.
- **Test data.** No real client or shop name: `Table 4`, `Table 5`,
  `Till 2`, `h1`/`h2`. No secret-shaped literal; `bearer-t2` is the
  pre-existing fake token this file's other tests already use.

## Verified beyond automated tests

- `go build ./...`, `go vet ./...`, `gofmt -l .` (no output),
  `go test ./...` (**every** package, not just the two touched) — all run
  by this reviewer directly, green both before and after the review fixes.
- `golangci-lint run ./internal/pages/...` — 0 issues.
- Guards run independently and green: `guard-data-access.sh`,
  `guard-i18n.sh`, `guard-page-http-error.sh`, `guard-kiosk-engine.sh`,
  `guard-compliance-claims.sh`, `guard-help-topics.sh`.
- **TDD claim re-verified by mutation, not just by revert.** Restoring
  both pre-fix source files while keeping the new tests gives a compile
  error (the tests reference the new field and the third return value),
  which is admissible evidence but weak — it proves only that the
  signature changed. So each half of the fix was reverted *behaviourally*
  instead, keeping the new signature so the suite still compiled:
  - re-introducing the replica-side `if h.UpdatedAt == "" { ... }` stamp
    fails `TestHeldSaleWriteThrough_ReplicaUpsertsOnPrimaryAndMirrorsLocally`
    with a real assertion error naming the stamped value
    (`got "2026-09-16 00:09:46"`);
  - removing only the primary-side stamp fails
    `TestSyncHeldSales_BlankUpdatedAtIsStampedByPrimaryClock` on "the
    primary must report the value it actually stamped".

  Both pass again with the fix restored. The review's own F2 fix was held
  to the same standard: its test fails with a real assertion error when
  the non-blank guard is removed.
- **Traced the SQL by hand** rather than trusting the doc comments, for
  both `Upsert` (confirms `h.UpdatedAt` is ignored on *both* branches, so
  the fallback paths are untouched) and `UpsertIfNewer` (confirms
  `excluded.updated_at` in the `DO UPDATE` resolves to the *coalesced*
  VALUES expression, which is what makes the pre-fix-primary story above
  safe).

## Known limitation of this review

`guard-deadcode-baseline.sh` could not be executed in this environment —
it builds under the `desktop` tag and the container has no GTK/WebKit
headers (`Package 'gtk+-3.0' ... not found`), the same environmental gap
`CLAUDE.md` already records for `cmd/unitill-desktop`. The failure is
environmental, not a finding: this change adds no function and removes no
call site, so it cannot orphan anything. `heldSaleTimeLayout` in
particular still has live non-test users (`sync_held_sales.go`).

## Explicitly deferred (accepted, not blockers)

- **SQLite's one-second resolution** — the other half of the ADR-0093
  Decision 2 residual. `datetime('now')` and `heldSaleTimeLayout` are both
  second-granular, so two writes inside the same one-second window compare
  equal and both apply (equal-applies is deliberate — it is what makes an
  idempotent retry succeed). This card does not address it, per the
  product owner's own card text, and it is *not* made worse here: with
  both sides of the comparison now stamped by the same clock, a same-second
  tie resolves in the primary's own single-writer serialisation order,
  which is strictly more defensible than the previous cross-clock tie.
- **A caller-supplied `updated_at` is still trusted verbatim** by the
  upsert endpoint. No production caller sends one after this card, so the
  skew hole is closed in practice; but the endpoint would still honour a
  forged or skewed value from a future or third-party caller. Left as-is
  deliberately: it is unchanged behaviour, it is what an existing test
  (`TestHeldSaleWriteThrough_RefusalStillMarksPrimarySynced`) uses to
  synthesise a deterministic refusal, and closing it properly means the
  bigger primitive — a primary-assigned monotonic version — that ADR-0093
  already names as the real fix.
- **A full per-line merge** of two tills' simultaneous edits to the same
  order remains ADR-0093's explicit non-goal; this is still
  last-writer-wins with a clean, detectable refusal.
