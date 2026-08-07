# Replica kiosk dead-end: cross-till "Edit on the primary" link (ut-docs#390)

## What shipped

Field report: on a replica till, the read-only Catalog/Inventory banner's
"Edit on the primary" link opened the primary till's own UI in the **same**
kiosk browser (fullscreen Chromium under `cage`, no chrome, no tabs, no
back button). Following it stranded the operator on a different physical
till's session with no way back — the third instance of this kiosk
dead-end class, after ut-docs#147/#148 and #159.

Fix follows the exact pattern #159 already established for the
structurally identical status-bar download-link case: reuse
`selfupdate.DownloadLinkActionable` (true only on `windows`/`darwin`) to
gate whether the banner's primary-till link renders as a live
`target="_blank"` anchor or falls back to inert, informational text.

- `internal/httpx/httpx.go`: new template func `crossdevicelinkactionable`,
  a second name over the same underlying predicate as `updatedownloadlink`
  — one shared decision of "does this install have real browser chrome,"
  per #159's own review record.
- `web/ui/pages/{inventory,catalog}.html`: the banner's `<a>` gated behind
  it; inert `<span>` fallback otherwise.
- `web/locales/{en,ar,fa,tr}.json`: new `sync.banner_open_primary_unavailable`
  key.
- `web/help/{en,ar,fa,tr}/multitill.md`: new step describing the read-only
  banner and its platform-dependent link behavior (product-owner standing
  instruction, ut-docs#324).
- Regression coverage across two layers: `internal/pages/inventory_sync_banner_test.go`
  (new) and `internal/pages/catalog/handlers_test.go` exercise the real
  route/handler path on this (Linux) runtime; `internal/httpx/sync_banner_test.go`
  (new) directly renders both templates with the platform predicate
  overridden both ways, covering the branch the handler-level tests can't
  reach without controlling `runtime.GOOS`.
- `internal/pages/catalog/main_test.go` (new): the `catalog` package had no
  `TestMain` wiring real i18n (the same class ut-docs#303 already found and
  fixed once for the sibling `pages` package) — needed so the new
  regression test asserts on real translated text, not `httpx.T`'s
  bare-key fallback.

Full-app audit for the same link class (`target="_blank"`/`window.open`/
cross-origin `window.location`) found no other occurrence — `tills.html`'s
own `{{ if .SyncPrimary }}` block is a different feature (a
promote-to-primary form posting to the local till's own API, no
cross-device navigation).

## Independent review (fresh subagent, `complexity:medium` → Opus)

**Verdict: yes-with-fixes-below.** Confirmed correct, minimal, and
genuinely solves the field-reported problem. Two should-fix findings,
both addressed before merge; four nits, triaged below.

1. **Should-fix — only the "no link" branch had regression coverage.**
   Verified by the reviewer via mutation: deleting the entire
   `{{ if }}/{{ else }}` from both templates (permanently removing the
   working link for Windows/macOS operators) left every existing test and
   all three CI guards green — `guard-i18n.sh` silently dropped the
   now-orphaned `sync.banner_open_primary` key without complaining. Fixed:
   added `internal/httpx/sync_banner_test.go` with four tests
   (`{Inventory,Catalog}Banner{LinksToPrimaryWhenActionable,HasNoLinkWhenNotActionable}`),
   directly rendering both templates via `httpx.NewRenderer`/`FuncsFor`
   with `crossdevicelinkactionable` overridden both ways — mirroring
   `template_helpers_test.go`'s own existing pair for the #159 precedent
   exactly. Re-verified myself: reproduced the reviewer's exact mutation
   (link permanently removed) and confirmed the two new
   `LinksToPrimaryWhenActionable` tests fail with the expected message;
   restored, both pass again.
2. **Should-fix — no manual update, no code-review record.** Fixed: added
   step 7 to `web/help/{en,ar,fa,tr}/multitill.md` describing the
   replica's read-only banner and its platform-dependent link behavior.
   This record is the code-review record.

Nits, triaged (none blocking, per this pipeline's one-review-round rule —
none are money/tax/data-loss/security class, so no second review round is
owed):

3. **Accepted, not fixed.** The commit message's "windows/darwin, where
   real recoverable browser chrome exists" overstates the guarantee: the
   Windows/Linux desktop shell (`cmd/unitill-desktop/webview_fallback.go`)
   still has no `NewWindowRequested`/`NavigationStarting` handler, so a
   Windows desktop install would still show a `target="_blank"` popup with
   no address bar if it were ever followed. Pre-existing gap, already
   filed as a deferred Backlog item off #159's own review — not a
   regression introduced here, not this diff's job to close.
4. **Accepted, not fixed — flagged for a follow-up card.**
   `internal/pages/sync_admin.go:114` passes an unused `"primaryURL"` key
   into `sync_chip.html`'s data map; the partial never renders it (its
   replica branch is already inert text). Pre-existing dead data, not
   introduced by this diff, but it's a live cross-device URL sitting in a
   template's data map — a future edit rendering it would be occurrence #3
   of this exact bug class. Worth a small follow-up card to delete or
   annotate; out of scope for this fix.
5. **Accepted, not fixed.** `internal/pages/catalog/handlers_test.go`
   inlines a `CREATE TABLE settings (...)` DDL block rather than adding it
   to `internal/testsupport/sqlite_catalog.go` alongside the rest of that
   test DB's mirrored schema. Legal (`_test.go` is excluded from
   `guard-data-access.sh`) and verified byte-identical to
   `internal/db/migrations/001_init.sql:18-22` — but the next catalog test
   needing `settings` will likely copy it again rather than share it.
   Minor, deferred.
6. **Accepted, not fixed.** New EN string "Edit this on the primary till"
   vs. the sibling key's "Edit on the primary" vs. the manual's own
   consistent "main till" wording — pre-existing terminology drift in this
   corner of the copy, not introduced by this diff. Not chasing a
   copy-consistency pass here.

**Predicate-reuse soundness (reviewer's point 4, explicitly answered and
independently verified):** the reviewer checked whether the app's
purpose-built `kiosk` template func (`UT_KIOSK` env var) would have been
the more principled signal, and found it's **never set** anywhere in
`packaging/linux/unitill-kiosk-setup.sh` or the systemd unit that actually
provisions field kiosk hardware — only mentioned in a DIY-build doc. Gating
on `kiosk` would have shipped a no-op fix on the exact hardware that
generated this report. Reusing `selfupdate.DownloadLinkActionable` is a
sound risk-direction choice (correct on the reported kiosk case, safe on
the false-negative Linux-desktop case, known-imperfect only on the
already-deferred Windows-desktop-shell case per nit 3) — agreed with, not
overridden.

## Verified beyond the reviewer's own pass

- `go build ./...`, `go vet ./...`, `gofmt -l` on all touched `.go` files —
  clean.
- `bash scripts/ci/guard-data-access.sh`, `guard-i18n.sh` (842 keys, all
  locales match `en.json`), `guard-help-topics.sh` — all green after the
  manual addition.
- `go test ./...` — clean except the same pre-existing, unrelated failure
  already documented in prior review records
  (`internal/issuereport.TestSaveCleansUpDirectoryOnWriteFailure`, caused
  by this sandbox running as root).
- Re-ran the reviewer's own TDD verification independently (see finding 1
  above) rather than taking the subagent's word for it.

## Safe to merge

Yes. Feature branch `fix/390-replica-crosstill-link-deadend`, merged via
`merge` (not squash/rebase, per this pipeline's standing merge-method
rule, ut-docs#250).
