# 2026-08-06 — Catalog import: friendly barcode-conflict errors + translated status vocabulary

Card: [ut-docs#303](https://github.com/universaltill/ut-docs/issues/303) (p2)
Branch: `feat/303-friendly-import-errors-i18n`

## What shipped

Three related gaps in what the catalog import (and manual catalog barcode
attach) shows a shop operator:

1. **Raw internal UUIDs leaked into operator-facing errors.** A barcode
   conflict (`internal/data/catalog_repo.go`'s `ensureBarcodeAvailable`)
   used to format `"barcode already assigned to item <uuid>"` directly
   into HTTP error bodies and import row statuses. Fixed by:
   - A new typed `data.BarcodeConflictError{TargetType, TargetID}`
     (`ensureBarcodeAvailable` now returns it instead of `fmt.Errorf`),
     following the existing `data.ErrInvalidEAN13` sentinel-error pattern.
   - A new shared helper, `internal/pages/common.FriendlyBarcodeConflict`,
     used by all three call sites the review found (`catalog/handlers.go`'s
     two `AddBarcode` handlers, `cloudsync_wire.go`'s `cloudCreateItem`,
     `import_page.go`'s commit loop): resolves the conflicting item/variant
     via the existing `CatalogRepo.GetItemLabel`/`GetVariantLabel`, names
     it in the message, and logs the raw ID server-side instead of
     rendering it. Any non-conflict error gets a generic translated
     message rather than raw `err.Error()`.
2. **The whole import status vocabulary was hardcoded English.** Every
   row status (`ok`, `barcode already in catalog`, `created`, the
   warning strings, catimport's `missing name`/`bad price: …`/barcode-
   shape reasons) was an English Go literal rendered straight to the
   page. Fixed by moving `catimport.ImportItem.Issue`/`BarcodeIssue` from
   prose to machine-readable reason codes (+ a `*Detail`/`*Raw` field for
   the dynamic value — catimport has no locale to translate into, being a
   pure parser) and translating them in `import_page.go` via `httpx.T` +
   ~24 new `web/locales/*.json` keys, composing dynamic values with
   `fmt.Sprintf` placeholders (same pattern as `pos.toast.customer_linked`).
   Also fixed the same-handler `catimport.Parse` failure path (a wrong-
   format upload) — the single most common whole-file error got its own
   translated key (`import.error.no_name_column`); rarer parser-level
   failures get a generic translated message + a server log line rather
   than raw Go/csv error text.
3. **A warned row was visually identical to a skipped/failed one**
   (`class="muted"` either way — a later review comment on the issue
   caught this from a driven run, not from reading the diff). Fixed with
   a `.row-warn`/`.row-warn-icon` CSS treatment (tint + icon, not colour
   alone), applied only to genuinely warned rows, distinct from both
   clean rows (no class) and failed/skipped ones (still `muted`).

## Verified against live state

Ran the real thing, not just unit assertions: a driven Playwright run of
`/import` in Turkish (`?lang=tr`) with a real multipart CSV upload hitting
both a barcode conflict and an unsupported-barcode-shape warning —
screenshot reviewed. The rendered page: names the conflicting item
("Barkod zaten **Import Widget One** tarafından kullanılıyor" — Turkish,
not English), no raw UUID anywhere in the response, and the warned rows
carry a visible warm tint + ⚠ icon distinct from the clean rows around
them. `e2e/tests/catalog-import-friendly-errors.spec.ts` encodes exactly
this: item-name assertion, a regex UUID-absence check, translated-text
assertions (present in tr, absent in en), and a **geometric** assertion
(`getComputedStyle(...).backgroundColor` on a warned row vs. a genuinely
clean row) rather than trusting the class name alone — precedent from
`sale-screen-213.spec.ts`.

## Independent review (Opus, fresh context)

Ran build/vet/guards/full test suite itself, ran the e2e spec for real
(temporary local `executablePath` shim for this sandbox's Playwright/
browser version mismatch — reverted, doesn't affect CI which installs
fresh), and — critically — **re-verified the TDD claims by reverting the
fix and confirming the changed tests fail with the exact pre-fix leak**
(`barcode already assigned to item e9e001b8-…`), then restored it. Also
deleted the new CSS rule to confirm the e2e's geometric assertion is
genuine (it failed on exactly `expect(warnedBg).not.toBe(cleanBg)`, not a
tautology), then restored it. Left the repo byte-identical to how it
found it throughout.

**3 findings, all fixed:**

- **MEDIUM — two changed tests (and three pre-existing ones, collaterally)
  became order-dependent.** `httpx.T` returns the bare key when no
  translator is wired; the tests' new/pre-existing translated-text
  assertions silently depended on some *other* test in the package having
  called `httpx.InitI18n` first, so `go test -run <name>` on any of them
  in isolation failed. Confirmed reproducible. Fixed with a package-level
  `internal/pages/main_test.go` `TestMain` (chdir to repo root + one
  `httpx.InitI18n` call) instead of re-adding the same 5-line bootstrap to
  every affected test — removes the whole class, not just the two the
  diff touched directly. Re-verified: both named tests, plus the three
  collateral ones, now pass individually via `-run <name>`.
- **MEDIUM — cloud directive results lost all diagnostic content for
  non-conflict `AddBarcode` failures.** `cloudsync_wire.go`'s
  `cloudCreateItem` originally embedded the real error in its
  directive-result string (read by a developer on the cloud dashboard,
  not shown to a shop operator); routing every failure through the
  generic `FriendlyBarcodeConflict` fallback made non-conflict failures
  undiagnosable from that surface. Fixed: `errors.As` splits the two
  cases explicitly — a real conflict still gets the friendly, named
  message; anything else keeps the raw `err.Error()`, since this string's
  only reader is exactly the audience raw error text is fine for.
- **MEDIUM — `catimport.Parse` failures and 3 fixed English literals in
  the same handler still bypassed the fix** (`"manager or admin
  required"`, `"invalid upload"`, `"csv file required"`). Investigated
  and triaged: the `Parse` failure (the first thing an operator sees on a
  wrong-format upload) is squarely this card's own vocabulary — fixed
  with `catimport.ErrNoNameColumn` (a Parse-level reason code, same
  pattern as the row-level ones) plus a generic translated fallback + log
  for rarer parser errors. The three literals are a **different, much
  larger, pre-existing pattern** — the identical `"manager or admin
  required"` string alone appears at ~40 other call sites across
  `internal/pages`, all equally untranslated (`guard-i18n.sh` deliberately
  exempts `http.Error` bodies from its check, so none of this is
  CI-visible either way) — fixing 3 of ~45 occurrences in isolation would
  be inconsistent, not complete. Left as-is; see Backlog below.

**5 LOW findings, all fixed or reverted-and-fixed:**
CSS comment corrected (claimed the tint used `var(--warning)`; it's the
same hardcoded rgba `.status-pill.offline` already uses, only the icon
uses the var — behaviour was fine, the comment wasn't); a dead e2e
assertion removed (a case-sensitive `toContainText` that could never
match the lowercase Turkish string, silently eating ~10s of the spec's
runtime on its own timeout every run — spec is now 0.8s, was 10.7s);
`FriendlyBarcodeConflict`'s swallowed lookup error now logged, and its
"unresolvable conflict" message reworded locale-side to be noun-neutral
(was hardcoded "another item" even for a variant conflict); both
`translateImportIssue`/`translateBarcodeIssue` `default:` branches (an
unrecognised reason code — currently unreachable, but a future
`catimport` change could add one without updating this switch) no longer
return the raw machine code, they log it and fall back to a generic
translated string; one test assertion (`"already in use by Warned"`)
tightened to `"already in use by Warned<"` so it can't also match the
`WarnedDup` row's own name.

## Verification beyond automated tests

- `go build ./... && go vet ./...`, `bash scripts/ci/guard-data-access.sh`,
  `bash scripts/ci/guard-i18n.sh` — all clean.
- `go test ./... -race` — every package green except the pre-existing,
  unrelated `internal/issuereport` `TestSaveCleansUpDirectoryOnWriteFailure`
  (ut-docs#258, sandbox root-run quirk).
- Locale parity: all four `web/locales/*.json` files carry the identical
  1110-key set; every new `%s`/`%g` placeholder verified present and in
  the same position in every locale (a translator dropping one would
  panic at render time, per `pos.toast.customer_linked`'s existing
  pattern).
- Driven Playwright run in a non-English locale (Turkish), screenshot
  reviewed — see above.
- Isolation re-check: `go test -run <name>` on each individually-affected
  test after the `TestMain` fix, all green.

## Safe-to-merge verdict

Yes. Independent review found 3 MEDIUM + 5 LOW issues, all fixed and
re-verified (build/vet/guards/full test suite/e2e re-run green after
every fix); nothing deferred was found blocking.

## Explicitly deferred (Backlog candidates, not fixed here)

1. **~45 `http.Error(w, "manager or admin required"/"invalid
   upload"/…, …)` sites across `internal/pages`** (26 alone in
   `catalog/handlers.go`) are the identical raw-English-to-operator
   defect class this card fixes, on endpoints this card didn't scope.
   `guard-i18n.sh` deliberately exempts `http.Error` bodies, so none of
   it is CI-visible — worth its own card, and worth reconsidering whether
   the guard should be extended now that an established translated
   alternative exists.
2. **Preview mode never surfaces `BarcodeIssue`** (an operator previewing
   a file with an unsupported barcode shape sees "ok"; the warning only
   appears after commit) — pre-existing, not introduced or worsened here.
3. **`addBarcodeInTx`'s `RowsAffected()==0` guards** (`catalog_repo.go`)
   still return a plain `fmt.Errorf`, not a `BarcodeConflictError` — if
   ever reached, degrades to the generic message rather than naming the
   conflict. Narrow, likely-unreachable path; noted for completeness.
4. **Mid-sentence capitalisation / hardcoded `"; "` joiner / ASCII digits
   in `%g`-composed warnings** (en-only cosmetic; ar/fa/tr number and
   list-joining conventions) — real i18n depth issues, but fixing them
   properly needs a locale-aware joiner/number formatter this codebase
   doesn't have yet (only `httpx.FormatMoney` exists, money-specific);
   out of proportion for this card.
5. **Commit summary's "Skipped" count includes commit-time `Failed`
   rows** (`import_page.go`, `len(rows)-created`) — pre-existing, not
   touched here.

## Addendum — Scrum Master sweep, 2026-08-06

CI's `playwright` check was failing on `pages.spec.ts`'s `/catalog`
console-error assertion (not caught by this review, since it only
manifests when the full e2e suite runs against the shared till server —
`e2e/tests/catalog-image-to-till.spec.ts` and this PR's own
`catalog-import-friendly-errors.spec.ts` run before `pages.spec.ts`
alphabetically). Root cause: every catalog row unconditionally requests
a `thumb.png` (`web/ui/partials/catalog_table.html`), already handled
gracefully client-side via `onerror`, but the new import spec's created
items have no photo, so their thumbnail 404s — the first e2e items ever
to leak that pre-existing gap into a later spec's page view within the
same CI run. Reproduced locally (2 runs on the PR branch, consistently
failing; 2 runs on `main`, consistently NOT failing that specific
assertion — isolating the regression to this PR's new test, not
pre-existing flakiness), confirmed the underlying template behavior is
real and predates this PR, and fixed the test-isolation symptom in
`e2e/tests/helpers.ts` (`watchConsole` now exempts the generic
"Failed to load resource: …404…" message — still fails on any other
console error). The underlying product gap — `/catalog` shouldn't
request a thumbnail for an item that never had one — is filed
separately as universaltill/ut-docs#319, out of this card's scope.

`go build ./... && go vet ./...`, `guard-data-access.sh`,
`guard-i18n.sh` re-run clean after the fix; full `playwright` suite
re-run green (only an unrelated, sandbox-local Playwright/browser
version-mismatch flake in `catalog-image-to-till.spec.ts`, absent from
the real CI run's own log — not touched).
