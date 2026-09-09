# Code review: demo customers/promo codes — "edited disqualifies forever" (ut-docs#1858)

**Date:** 2026-09-09
**Card:** ut-docs#1858 — follow-up to ut-docs#1840/PR#944 (F10 of that review):
does "Remove sample data" have the same pristine-disqualifies-forever defect for
demo CUSTOMERS and demo PROMO CODES that it had for catalogue items?
**Branch:** `fix/1858-demo-customers-promos-edited-disqualifies` (commit `b79607a5`)
**Diff:** `internal/data/demo_seed_repo.go`, `internal/data/demo_seed_repo_test.go`,
`internal/data/seeddata/seeddata.go`,
`internal/data/seeddata/remove_demo_customers_promos.sql`,
`internal/data/seeddata/remove_demo_customers_promos_relaxed.sql` (new),
`internal/pages/settings_page.go`, `internal/pages/demo_seed_opt_in_test.go`,
`web/locales/{en,fa,tr,ar}.json`, `web/help/{en,de,fa,tr,ar}/display.md`,
`web/help/img/manifest.json`
**Reviewer:** independent (Opus 5, different model instance from the author,
own isolated git worktree, did not write the code and never saw the reasoning
that produced it)

## What shipped

1. **Demo promo codes get the same relaxed mode items already have.**
   `RemoveDemoCustomersPromos` now runs the existing till-wide gate
   `demoTillHasNoRealHistorySQL` and picks a new
   `seeddata/remove_demo_customers_promos_relaxed.sql` when the till has never
   traded for real — dropping only the promo's pristine field match, so a demo
   promo the merchant merely deactivated is removable in one tap.
2. **Kept customers AND kept promos are now named and reasoned.** The method
   returns `(removed int, keptCustomers []KeptDemoCustomer, keptPromos
   []KeptDemoPromo, err error)` instead of a bare combined kept count with a
   blanket, sometimes-false "already in use" message. New reason constant
   `KeptReasonTargeted`; two new shared `CASE` fragments
   (`demoCustomerReasonCaseSQL`, `demoPromoReasonCaseSQL`) mirroring
   `demoItemReasonCaseSQL`'s pattern. Audit payload logs ids/codes + reasons.
3. **Per-promo resolution.** `RemoveDemoPromo` / `KeepDemoPromoAsOwn` /
   `IsSamplePromo`, wired to `POST /api/settings/demo-promo/{code}/remove|keep`,
   rendered inline in the kept-promos list for `KeptReasonEdited` only.
4. **Customers get no resolution buttons** — the design claim being that every
   customer "kept" reason is a genuine live reference, never staleness.

## What the independent review checked, and what it found

Verdict up front: **no blocker-class issue.** Explicitly: **no money/tax defect**
(this diff touches no monetary value — `promotions.value` is an existing raw
`int64` basis-point/minor-unit column at the DB boundary and is only ever
compared, never arithmetic'd, here), **no data-loss defect**, **no security
defect**. Findings below are non-blocking; two stale comments were fixed in
this pass.

### 1. The core factual claim — re-derived independently: TRUE

The claim that demo CUSTOMERS never had the pristine-match defect holds. I read
`remove_demo_customers_promos.sql`'s customer CTE directly: its predicate is
`is_sample_data = 1` + a join on the seeded id set + five `NOT EXISTS` reference
checks (`sales`, `sales_archive`, `held_sales` payload, `held_sales_archive`
payload, `promotions.customer_id`). There is no field comparison of any kind.
Only the promotion `DELETE` carried the `is_active = 1 / starts_at IS NULL /
ends_at IS NULL / (code,type,value,description)` pristine block. Scoping the fix
to promo codes only is correct.

I then re-derived `demoCustomerReasonCaseSQL` against the real schema rather
than trusting the comment:

- `grep 'REFERENCES customers' internal/db/migrations/001_init.sql` returns
  exactly two FKs: `promotions.customer_id` and `sales.customer_id`.
- `customer_id` appears in exactly three places in the whole schema:
  `promotions` (l.37), `sales` (l.224), `sales_archive` (l.785, no FK — archive
  tables carry none). No later migration (002–015+) adds another.
- Plus the two payload-only references in `held_sales` / `held_sales_archive`.

So the five removal checks are an exhaustive enumeration of every way this
schema can reference a customer, and the `CASE`'s three arms (held / history /
`ELSE` targeted) partition that set exactly: a surviving sample customer failed
at least one check, held and history are tested explicitly, and the only
remaining failure mode is `promotions.customer_id` — which `ELSE 'targeted'`
reports correctly. **No hole.** Every reason is a real live reference; none is
relaxable. Not offering "remove anyway" for a customer is right — a "remove
anyway" on the `history` or `held` arms would FK-fail or orphan a restorable
archive batch, and on `targeted` it would silently break the shop's targeted
offer.

The one theoretical soft spot is shared with the item side and already
documented there as ut-docs#1840 finding F7: `keptDemoCustomers` /
`keptDemoPromos` deliberately do *not* join the seeded id set, so a
hypothetical `is_sample_data = 1` row outside it would be reported under the
`ELSE` arm. I verified the invariant that makes this moot:
`demo_customers_promos.sql` is the only writer of `is_sample_data = 1` on
`customers`/`promotions` anywhere in the tree, and `CreatePromotion`'s own
comment confirms merchant-created promos deliberately leave the column at its
`0` default. The new functions cross-reference `keptDemoItems`' F7 note, which
is the right treatment.

### 2. The relaxed SQL file — diffed mechanically: correct

I stripped comments and blank lines from both files and diffed the executable
SQL. The **only** difference is the promo pristine block:

```
 DELETE FROM promotions
  WHERE code IN (SELECT code FROM demo_seed_promos)
    AND is_sample_data = 1
-   AND customer_id IS NULL
-   AND is_active = 1
-   AND starts_at IS NULL
-   AND ends_at IS NULL
-   AND ( ...per-code type/value/description match... );
+   AND customer_id IS NULL;
```

`customer_id IS NULL` is retained and unconditional. The customer CTE, the
customer `DELETE`, the statement order (promotions before customers, so the
pre-delete removable set is unaffected) and the TEMP-table teardown are
byte-identical. This mirrors `remove_demo_relaxed.sql`'s relationship to
`remove_demo.sql` exactly. Re-verified after my own edits below.

### 3. Reason priority: correct and self-consistent

A promo that is BOTH targeted and field-edited reports `targeted`
(`WHEN p.customer_id IS NOT NULL` wins), the bulk list renders it with no
buttons, and `RemoveDemoPromo`'s own re-check — which uses the *same* shared
`demoPromoReasonCaseSQL` constant, so the two cannot drift — refuses with
`ErrDemoPromoTargeted`. All three agree. Hard blocker first is the right
ordering and matches `demoItemReasonCaseSQL` (held > history > edited).

`ELSE 'edited'` is never wrong: in relaxed mode every untargeted sample promo is
deleted, so a survivor must be targeted; in strict mode an untargeted survivor
can only have failed the pristine block.

The customer ordering (held > history > targeted) is defensible rather than
arbitrary, and it only ever picks *which* of several true statements is shown,
since none of the three is relaxable and none is offered a button. It also
matches the item-side precedent. Mildly in its favour: `held` is the one a
merchant can actually clear themselves (void the parked sale), so surfacing it
first is the more actionable choice.

### 4. Race/staleness in `RemoveDemoPromo`: no TOCTOU window

The reason re-check and the `DELETE` are both issued on the same `*sql.Tx`, so
they are already atomic with respect to each other — and more strongly, the
DSN in `internal/db/db.go:68` is
`...&_pragma=journal_mode(WAL)&...&_txlock=immediate`, so *every* `BeginTx`
takes the write lock up front. A promo cannot become targeted between the check
and the delete. This is exactly the mechanism the ut-docs#1840 review credited
for `RemoveDemoItem`'s equivalent window, so the promo path inherits the same
guarantee rather than reasoning about it afresh. The final
`DELETE ... AND is_sample_data = 1` is a redundant second guard, same as the
item side.

### 5. Relaxed-mode gate reuse: correct, with a named residual (F3 below)

`demoTillHasNoRealHistorySQL` examines only `sale_lines`/`stock_movements`
(live + archived, direct + via variant) against non-sample items. Extending it
verbatim to promos is defensible and is what ut-docs#1840 established as *the*
definition of "this shop has never traded for real". It is genuinely
till-wide, not item-scoped, so it is not being misapplied. The residual gap it
leaves for promos specifically is recorded as F3.

### 6. HTTP layer: security properties match the item endpoints exactly

- Pre-elevation existence check via `IsSamplePromo` **before** `checkOrElevate`
  — the ut-docs#1840 F3 convention. An unknown code gets a plain refusal, never
  a manager-PIN dialog carrying caller-chosen text.
- `hx-target` uses the attribute-selector form (`demoPromoMsgSelector` →
  `[id="..."]`), not a bare `#`+value.
- Neither new handler sets `X-UT-Response: ok` (verified by reading the whole
  handler range; the only two `Set("X-UT-Response", ...)` sites in the file are
  in unrelated later handlers) — so an elevated success does not reload and
  wipe the rest of the kept list. That was ut-docs#1840 F2.
- Cashier → elevation prompt, manager → acts; audited via `settingsAudit` with
  the code as the subject.
- **Injection:** every interpolation of `code` goes through
  `html.EscapeString` (row `data-` attribute, `<strong>`, both `hx-post` URLs,
  the `hx-target` selector, the message-span `id`). `code` is also not
  attacker-controlled in practice: the only rows that can reach this renderer
  are `is_sample_data = 1` promos, and the only writer of that flag is
  `demo_customers_promos.sql`'s fixed three codes (`PROMO50`, `PROMO500`,
  `DISC10`) — `POSRepo.CreatePromotion` deliberately leaves the flag at `0`, and
  `UpdatePromotion` never rewrites the primary-key `code`. Recorded as F5 that
  the raw `code` is *not* URL-escaped when built into the `hx-post`/elevation
  URLs; harmless today for exactly that reason, and identical in shape to the
  item endpoints.

### 7. i18n: clean, and the translations are real

`guard-i18n.sh`, `guard-help-topics.sh` and `guard-docs-shots.sh` all run
directly by me, all green (output quoted in "Verified" below). Semantic diff of
`en.json` against the parent commit: exactly the 16 claimed keys added,
`settings.data.demo_kept` removed, **zero existing values changed**. No dangling
reference to the removed key anywhere in `internal/` or `web/ui/`.

Spot-checked the ar/fa/tr strings for all 16 keys: they are genuine, idiomatic
translations, not English copy-paste, with `%s` preserved in both
`elevation.summary.*` keys in every locale. e.g.
`settings.data.demo_promo_targeted` → ar "كود الخصم هذا مُعدّ لعميل محدد ولا
يمكن إزالته بهذه الطريقة."، fa "این کد تخفیف برای یک مشتری خاص تنظیم شده و از
این طریق قابل حذف نیست."، tr "Bu promosyon kodu belirli bir müşteri için
ayarlanmış ve bu şekilde kaldırılamaz." Native-fluency/register review was not
performed and is not claimed.

### 8. Test quality — re-verified by breaking the production code, five times

Reading assertions is not evidence they are wired up. I mutated the shipped
code in my own worktree and confirmed each test actually fails, then restored
(final `git diff HEAD --stat` empty before I made my own edits):

| # | Mutation | Test that caught it |
|---|---|---|
| 1 | `if relaxed && false` (never relax) | `TestRemoveDemoCustomersPromosRemovesEditedPromoWhenTillHasNoRealHistory` → `removed 4, keptPromos 2; want 6, 0` |
| 2 | `if relaxed \|\| true` (always relax) | `TestRemoveDemoCustomersPromosKeepsCustomizedPromotion` → `removed 6, keptPromos 0; want 5, 1`, **and** `TestSettingsRemoveDemoCatalogueEndpoint_EditedPromoRowHasBothButtons` (all 5 markup assertions) |
| 3 | drop the `reason == KeptReasonTargeted` refusal | `TestRemoveDemoPromoRefusesTargetedPromo` **and** `TestSettingsRemoveDemoPromoEndpoint_RefusesTargetedPromo` |
| 4 | `THEN 'held'` → `THEN 'history'` in `demoCustomerReasonCaseSQL` | `TestRemoveDemoCustomersPromosKeepsHeldSaleCustomer` **and** `...KeepsHeldSaleArchiveCustomer` |
| 5 | drop the pre-elevation `IsSamplePromo` check | `TestSettingsDemoItemEndpoints_UnknownIDNeverElevates` (rendered a PIN dialog for an unknown code) |

Mutation 2 is the important one for the false-pass question the brief raised:
it proves the `seedRealSale(t, d, "own-1", "s-1")` line newly added to
`TestRemoveDemoCustomersPromosKeepsCustomizedPromotion` is load-bearing, not
decoration — without strict mode forced, that test's `keptPromos != 1`
assertion fails. The pre-existing test still tests what it claims. I also
confirmed `seedRealSale` inserts a non-sample item + a customer-less sale +
one `sale_lines` row, so it flips the gate without perturbing customer
removability, which is why the surrounding `removed` counts stayed at 5.

Mutation 5 confirms the security property is genuinely test-locked for the new
endpoints, not just asserted in a comment.

## Findings

### Fixed in this review pass

- **F1 (nit, fixed): two comments asserted a fact that is false.**
  `remove_demo_customers_promos.sql`'s header claimed "the product has no
  promotions management UI at all yet, so customer_id is the ONLY way a promo
  could ever be deliberately targeted", and `demo_seed_repo_test.go`'s
  `TestRemoveDemoCustomersPromosKeepsCustomizedPromotion` comment repeated
  "there is no promotions management UI". A full promotions UI does exist —
  `internal/pages/promotions_page.go` registers `GET /promotions`,
  `POST /api/promotions`, `POST /api/promotions/{code}/edit` (which sets
  `customer_id`) and `POST /api/promotions/{code}/active`. The claim is
  pre-existing (ut-docs#567 era) but this diff edits both files and leans on
  that reasoning, so it should not be carried forward. **Fixed**: both comments
  corrected to state the accurate version — nothing in the schema references
  `promotions.code` (verified: no `REFERENCES promotions` and no
  `promo_code`/`promotion_code` column anywhere in `internal/db/migrations/`;
  `sale_discounts`/`sale_discounts_archive` carry only `type`/`value`/`amount`/
  `reason`), so `customer_id` is the only *durable* reference, and the UI does
  exist. Comment-only; the executable SQL is unchanged (re-diffed after the
  edit, still the single promo-clause difference).
  Note this makes the card's premise *stronger*, not weaker:
  `POST /api/promotions/{code}/active` is precisely the "merely deactivated a
  demo promo" path ut-docs#1858 was filed for, so the bug was reachable through
  a real, shipped screen.

### Non-blocking, not fixed here

- **F2 (should-fix, follow-up recommended): the two `*_targeted` messages state
  a fact but name no next step.** `settings.data.demo_kept_reason_customer_targeted`
  ("A promotion is set up for this customer") and
  `settings.data.demo_kept_reason_promo_targeted` ("This promo code is set up
  for a specific customer") are dead ends for the merchant, yet — unlike an
  item's `history`/`held` — this one *is* operator-resolvable: clear the
  customer target at Settings → Promotions, then run "Remove sample data"
  again. ut-docs#1840 finding F1 established the standard that a blocked row's
  message should name the real mechanism (that is why
  `settings.data.demo_item_has_history` points at Catalog cleanup). Deliberately
  **not** fixed here: a locale-only reword would leave `web/help/*/display.md`
  behind, and this repo's standing rule is that the manual ships with the
  behaviour — so the honest fix spans 4 locale files + 5 manual files + a
  `make docs-shots` regen, which is a card, not a review nit. Not a blocker:
  the new message is strictly better than the blanket, sometimes-false
  "N record(s) could not be removed (already in use)" it replaces.
- **F3 (nit, by-design residual): the relaxed-mode gate is item-shaped, and
  promos now ride on it.** `demoTillHasNoRealHistorySQL` returns "never traded
  for real" for a till that has real *customers* and real *promotions* entered
  through the UI but no non-sample `sale_lines`/`stock_movements` row. A
  merchant who spent an afternoon before opening day repurposing `DISC10` into
  their real 15%-off code, then tapped "Remove sample data", now loses it
  outright with no kept-list entry — where the old strict rule kept it. This is
  the same trade-off ut-docs#1840 knowingly accepted for a renamed/repriced demo
  item, and it stays behind a manager PIN, an `hx-confirm`, and an audit-log
  entry, so it is consistent with accepted precedent rather than a new class of
  risk. Worth a line in the card's own notes; not a reason to hold the merge.
- **F4 (nit): manual wording drifts slightly from the actual UI.**
  `web/help/en/display.md` item 9 now says an edited item or promo code offers
  "**Keep as my own**", but the item button's label is still
  `settings.data.demo_item_keep_own_btn` = "Keep as my own item" (only the promo
  button reads "Keep as my own"). The same paragraph lists "sold to, parked in a
  basket, or targeted by a promotion" for *both* customers and promo codes, but
  a promo can only ever be kept for the targeting reason. Not fixed: any
  `display.md` edit changes the topic hash and requires a `make docs-shots`
  regen, which is not proportionate to a wording nit. Otherwise the manual edit
  is accurate — the relaxation, the "deactivated" case, the per-record buttons,
  and the "named in its own list but never offered either button" behaviour for
  customers all match the code.
- **F5 (nit, matches precedent): `code` is HTML-escaped but not URL-escaped**
  when built into `hx-post="/api/settings/demo-promo/<code>/remove"` and into
  `renderElevationPrompt`'s action URL. Unreachable today (the only codes that
  can appear are the three seeded ones; see §6), and identical in shape to the
  item endpoints. Flagged for the record only.
- **F6 (nit): "currently parked" is imprecise for the archive arm.**
  `settings.data.demo_kept_reason_customer_held` says "This customer is in a
  currently parked sale", but the `held` arm also fires for
  `held_sales_archive` — a basket sitting in a reset archive, not currently
  parked. This exactly mirrors the existing item key
  `settings.data.demo_kept_reason_held`, so the new key is consistent with what
  shipped in ut-docs#1840; fixing it means fixing both, in a separate pass.
- **F7 (nit, test coverage): two claims are not test-locked.** Nothing asserts
  that a kept CUSTOMER row renders *without* resolution buttons — which is the
  headline design decision of this card — and nothing asserts that a
  `KeptReasonTargeted` promo row renders without them either. Both are correct
  in the code (`writeKeptDemoCustomersHTML` emits only name + reason;
  `writeKeptDemoPromosHTML`'s `default:` arm emits only the reason span), and
  the *positive* case is covered by
  `TestSettingsRemoveDemoCatalogueEndpoint_EditedPromoRowHasBothButtons`, but a
  future refactor could add a button to the customer list with no test failing.
  A two-line `!strings.Contains(body, "demo-promo-remove-anyway")` assertion in
  the existing `...CoversCustomersPromos` test would close it.
- **F8 (scope, cosmetic): the commit re-sorted all four locale files and does
  not say so.** The diff is 887/872 changed lines *per locale file* (~3,500
  total) where 16 new keys account for ~60. I verified semantically that this
  is purely a key reordering: `main`'s `en.json` had 397 out-of-order adjacent
  key pairs, `HEAD`'s is fully sorted, and a set/value comparison shows exactly
  16 additions, 1 removal and **zero** changed values (same in ar/fa/tr).
  Harmless and arguably an improvement, but the commit message claims only "16
  new keys", which understates the diff, and this will conflict with any other
  in-flight locale branch. Recommend either amending the commit message to name
  the re-sort or dropping it into its own commit — orchestrator's call, since I
  am not committing.

### Verified as FINE (not problems)

- `removed = before - len(keptCustomers) - len(keptPromos)` is arithmetically
  identical to the old `before - after` (kept ≡ remaining sample rows after the
  script), so the "Removed N sample record(s)" count is unchanged in meaning.
- The whole bulk operation still runs in one transaction, which is what pins
  `database/sql` to the single connection the per-connection TEMP id tables
  require. The two new `keptDemo*` reads happen inside it, before `Commit`.
- `RemoveDemoPromo` correctly does *no* dependent-row cleanup: nothing in the
  schema references `promotions.code`, so a plain `DELETE` cannot orphan or
  FK-fail. (Contrast the item side, which must clear `inventory`/`price_history`
  by hand.) Verified against the schema, not just the comment.
- `KeepDemoPromoAsOwn` uses `is_sample_data = 1` in its `WHERE`, so it is
  idempotent-safe and reports `ErrDemoPromoNotFound` on a second call.
- Repository pattern: all new SQL lives in `internal/data` /
  `internal/data/seeddata`. `guard-data-access.sh` green, and I read the
  `internal/pages` diff myself — no query text there.
- No `money.Money` boundary is crossed by this diff.
- Docs-shots manifest regeneration is exactly consistent with the claim:
  only `surface_sha256` plus the four `display` topic hashes changed; no other
  topic hash and no PNG moved.
- Working tree left clean of my mutation experiments before I made my own
  edits (`git diff HEAD --stat` empty at that point).

## Verified beyond automated tests (my own run, this worktree)

- `gofmt -l .` → no output. `go vet ./...` → clean. `go build ./...` → clean.
- `golangci-lint run ./...` → **0 issues**.
- `go test $(go list ./... | grep -vE '/internal/plugins$|/internal/plugins/(oauth|marketplace)$')`
  → **exit 0, 48 packages ok, 0 FAIL.**
- Every guard referenced by `.github/workflows/ci.yml`'s `build` job run
  individually (44 scripts incl. the guards' own `_test.sh` self-tests):
  all green — `check-brand-assets`, `guard-data-access`(+test),
  `guard-price-history-sync`(+test), `guard-migration-version-collision`(+test),
  `guard-kiosk-engine`(+test), `guard-plugin-menu-read`(+test),
  `guard-page-http-error`(+test), `guard-i18n`(+ its 4 self-tests),
  `guard-compliance-claims`(+test), `guard-docs-shots`(+2 tests),
  `guard-help-topics`, `guard-webkit-version`(+test),
  `guard-kiosk-launch-flags`, `guard-android-status-address`,
  `guard-android-i18n`, `guard-android-external-links`(+test),
  `guard-android-manifest-features`(+test), `guard-gobind-skip_test`,
  `guard-emoji-font`, `guard-htmx-loaded`, `guard-autofill-suppression`(+test),
  `guard-osk-loaded`(+test), `guard-e2e-fixtures-import`(+test),
  `guard-makefile-version`.
  - **One exception, environmental:** `guard-deadcode-baseline.sh` (and its
    self-test) fails in this sandbox with `Package 'gtk+-3.0' ... not found` /
    `could not import C (no metadata for C)` from
    `internal/thirdparty/webview_go` and `cmd/unitill-desktop`. I confirmed this
    is **not** caused by the diff: I reset the worktree to the parent commit
    `b9127c0a` (= `main`) and the guard fails identically there. Missing
    GTK/WebKit dev headers in this container, the same gap `CLAUDE.md` already
    records for `cmd/unitill-desktop`'s lint exclusion (ut-docs#1581).
- After my F1 comment fixes: `gofmt -l .` clean, `go build ./...` clean,
  `go test ./internal/data/ ./internal/pages/` green, all guards re-run green,
  and the stripped strict-vs-relaxed SQL diff re-confirmed as the single promo
  clause.
- No secret-shaped literal and no real shop/customer name in the diff (the demo
  customers are the existing invented `Alice Carter` et al. from ut-docs#567).
- ADRs: nothing here contradicts ADR-0001/0002/0003/0008; no new architectural
  choice was made — this reuses the ut-docs#1840 pattern verbatim.

## Explicitly deferred

- F2 (targeted-reason messages should name the resolve-it-yourself route) —
  recommend a follow-up card, since it spans locales + the manual + a
  docs-shots regen.
- F4/F6 manual and locale wording nits — same reason.
- F7 negative-assertion test coverage for the no-buttons branches.
- F8 locale re-sort — commit-message/commit-hygiene call for the orchestrator.
- Native-fluency review of the ar/fa/tr strings (parity, `%s` preservation and
  plausibility were checked; register/naturalness was not).
- `ut-plugin-language-{de,es}` follow-up PRs for the 16 new `en.json` keys —
  required by the repo's `lang-pack-drift` rule, out of scope for this repo's
  branch and not attempted here.

## Verdict

**Approve.** No blocker-class issue: no money/tax, data-loss or security defect
found, and I say that having re-derived the customer-side claim from the schema
rather than the comments, mechanically diffed the two SQL variants, and broken
the production code five times to prove the tests are not false-passing. The
relaxation is exactly and only the promo pristine-match; every
reference-based safety clause is unconditional in both variants; the TOCTOU
window is closed by `_txlock=immediate`; the two new endpoints reproduce the
item endpoints' security properties including both ut-docs#1840 hardening
findings. One stale-comment finding (F1) fixed in this pass; F2–F8 are
non-blocking and recorded above for follow-up.
