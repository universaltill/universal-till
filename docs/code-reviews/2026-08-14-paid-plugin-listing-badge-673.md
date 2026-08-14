# 2026-08-14 — Paid plugin listing badge + entitlement error (ut-docs#673)

## What shipped

`ut-docs#673` — "Paid plugin listings are plumbed but inert" — `PaidListing`
already flowed end-to-end from `ut-cloud` through the till's marketplace
client, but nothing displayed it and a blocked download (paid listing
needing approval, or a free one not yet approved) surfaced as a generic
"download failed: status 403" instead of the cloud's specific reason.

- `internal/plugins/marketplace/client.go`: `IssueDownloadToken` used to
  discard the response body on any non-200 status *before* decoding it,
  throwing away the cloud's `{code, message}` error envelope even when one
  was sent. Now decodes first; a decoded `error` field returns a new
  `*APIError{Code, Message}` (an `errors.As`-able type), falling back to the
  old status-code error only when the body genuinely isn't the JSON envelope
  (confirmed still exercised — see TDD re-verification below).
- `internal/pages/plugins_store_page.go`: `storeItem` gets `Paid bool` from
  the catalog's `PaidListing` flag. The store's `/api/plugins/store/download`
  handler recognizes `APIError.Code == "not_entitled"` and responds with the
  `plugins.install.error.not_entitled` i18n key (403) instead of a generic
  502.
- `internal/plugins/install_status.go`: `ClassifyInstallError` — used by the
  Update button (`/plugins`), the replica-sync path, and
  `install-from-marketplace`, none of which the initial diff touched — gets
  the same `not_entitled` classification (review finding, see below).
- `web/ui/pages/plugins_store.html` / `web/public/app.css`: a `.paid-badge`
  next to the existing trust badge, styled identically to `.trust-badge`
  (font-size/padding/border-radius), a distinct color only.
- `web/locales/{en,ar,fa,tr}.json`: 3 new keys
  (`plugins.install.error.not_entitled`, `plugins.store.paid_badge`,
  `plugins.store.paid_hint`), translated into all four locales directly by
  this pipeline (the self-hosted NAS Ollama at 192.168.1.231 is unreachable
  from this cloud session — same constraint as ut-docs#684 — matching the
  immediately-preceding same-day commit's practice, `5b114b6`/ut-docs#661).
- `web/help/{en,ar,fa,tr}/plugins.md`: a new step documenting the Paid badge
  and the approval-needed message.

**Explicitly out of scope, split into follow-up cards at BA time**:
showing an actual price (no price/amount field exists in the data model —
only a paid/free boolean; ut-docs#700), and entitlement-lapse/revocation
handling for an *already-installed* paid plugin (no periodic re-check
mechanism exists; a real design task — ut-docs#701).

## Independent review

Fresh-context Opus subagent, worktree-isolated (`ut-docs`
"Model routing by complexity" — this card is `complexity:medium`, Sonnet
built it, Opus reviewed it). Ran `go build`, `go vet`, the full `go test
./...`, and all five repo guards (i18n, help-topics, data-access,
kiosk-engine, plugin-menu-read) live — all green. Findings, triaged:

1. **MEDIUM — fixed in this commit.** The `not_entitled` fix only covered
   the store/download handler; `internal/plugins/install_status.go`'s
   `ClassifyInstallError` — the classifier the Update button
   (`plugin_api.go`), the replica-sync path (`cloudsync_wire.go`), and
   `install-from-marketplace` all actually call — had no case for it, so
   those three paths kept showing the generic retryable failure, **with
   `Retryable: true`** — an operator clicking Update on a paid listing they
   aren't entitled to would see a Retry affordance that can never succeed.
   Fixed by adding an `errors.As`-based case to `ClassifyInstallError`
   itself (checked by type, not string-matching like its sibling cases,
   since the function already receives the real error) — one change fixes
   all three call sites at once. TDD: `TestClassifyInstallError_NotEntitled`
   added first, confirmed failing (`message key = "plugins.install.error.retryable"`,
   `want plugins.install.error.not_entitled`) against the un-fixed
   classifier, then passing after the fix, for both a bare and a
   `fmt.Errorf("...: %w", …)`-wrapped `APIError` (every real call site wraps
   it).
2. **LOW–MEDIUM, cross-repo (`ut-cloud`) — filed as ut-docs#703, not fixed
   here.** An unknown/deleted listing ID returns the same `not_entitled`
   code as a real approval gate, so the till's new specific message ("ask
   your merchant manager to approve it") can now be confidently wrong for a
   stale-catalog case. Needs a distinct code on the `ut-cloud` side; the
   till already has `plugins.install.error.not_found` ready to receive it.
3. **LOW, cross-repo (`ut-cloud`), pre-existing — filed as ut-docs#704, not
   fixed here.** REST `writeServiceError` has no case for
   `ErrRegisteredRequired` (the gRPC path does); falls through to a raw 500
   instead of an actionable message. Adjacent to this card's bug class but
   not caused by it.
4. **LOW (docs) — fixed in this commit.** The manual said the
   approval-needed message appears when "trying to install" a paid listing;
   the gate actually fires on **Download** (Install only becomes available
   after a successful download). Corrected in all four locale help topics.
5. **NITPICK — accepted, not changed.** "merchant manager" in ar/fa reads as
   the till's *own* manager rather than a marketplace-portal role — but this
   mirrors the pre-existing `plugins.store.empty`/`manager_approval_notice`
   phrasing already shipped, so fixing it here would mean retranslating
   established strings outside this diff's scope, not just the new ones.
6. **NITPICK — accepted, pre-existing, not changed.** The raw synchronous
   HTTP error text (`"download failed: %v"` / `"Update failed: %v"`) is
   still unlocalized English; the persisted status record (what the store
   card actually renders from) now carries the correct key after finding
   #1's fix, which is the operator-facing surface that matters.

Verified clean, no action needed: no new filesystem writes (the two
recurring bug classes — missing `os.MkdirAll`, cwd-relative paths instead of
`paths.Data(...)` — don't apply); the decode-before-status reorder in
`client.go` was traced through every branch (200+data, 200+error-envelope,
200+bad-JSON, non-200+JSON-error, non-200+non-JSON) and no existing caller
or test broke, including `client_more_test.go`'s pre-existing "non-200" case
(a plain-text 403 body, still falls through to the old status-code error, as
it should); `errors.As` unwraps correctly through every real call site's
wrapping; the catalog cache round-trips `PaidListing` (custom unmarshaller
already accepted both casings); CSS has no `left`/`right` properties (RTL-safe
by construction, confirmed no dark-theme variant exists to have missed
either); locale string lengths pose no layout risk; help-topic renumbering
is correct and not left in English in ar/fa/tr; no real client/shop name, no
secret-shaped literal.

## Verified beyond automated tests

- Real driven run (Tester step): the actual `plugins/store` page rendered
  through a live `httptest` server with the real templates/CSS, screenshotted
  via headless Chromium at 1024×700 — badge sits cleanly next to the trust
  badge, no overlap/wrap, exactly one badge rendered for the one paid listing
  in the fixture catalog (also pinned as a render-test assertion). RTL (fa)
  locale text itself wasn't confirmed live — the manual preview harness
  doesn't wire through the app's real locale-detection middleware — but the
  markup is direction-agnostic by construction (flex + gap, no left/right
  properties), matching the pre-existing trust-badge pattern already proven
  RTL-safe elsewhere in this codebase.
- Full `go test ./...` (not just the touched packages) green both before and
  after the review fix.

## Safe-to-merge verdict

**Yes**, after the finding-#1 fix folded into this commit. Findings #2/#3
are genuine but cross-repo and correctly deferred; #4 fixed; #5/#6 accepted
as documented, low-priority, out-of-scope-for-this-diff items.
