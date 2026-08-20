# Code review: distinguish "listing not found" from "not entitled"

**Issue:** ut-docs#703 · **Repo:** universal-till (till/POS side; the
cloud-side change and its own review record live in
`ut-cloud/docs/code-reviews/2026-08-20-listing-not-found-error-code-703.md`).
**Complexity:** easy · **Built by:** Sonnet (inline) · **Reviewed by:**
Sonnet (fresh-context subagent, worktree-isolated).

## What shipped

`ut-cloud` used to return the same `not_entitled` error code for two very
different situations: a real "you're not approved for this paid/gated
listing" entitlement gate, and a listing ID that doesn't exist at all
(stale catalog cache, a listing removed from the marketplace). Since
ut-docs#673 wired the till to show a specific, confident message for
`not_entitled` ("ask your manager to approve it"), the second case showed
a wrong and confusing message — there's nothing to approve, the till just
needs a fresh catalog.

`ut-cloud`'s side of this fix (separate PR, reviewed independently) now
returns a distinct `listing_not_found` code, HTTP 404. This repo's change
teaches the till to recognize it and show the right message:

- `internal/plugins/install_status.go` — `ClassifyInstallError` gained a
  case for `apiErr.Code == "listing_not_found"` → the existing
  `plugins.install.error.not_found` message key ("The selected plugin was
  not found in the catalog."), `Retryable: false`. No new i18n key — this
  key already exists, fully translated, in all 4 shipped locales.
- `internal/pages/plugins_store_page.go` — the store/download handler's
  own inline `not_entitled` check (predates `ClassifyInstallError`'s fix,
  per ut-docs#673 — this handler doesn't route through it) got a parallel
  `listing_not_found` → 404/`plugins.install.error.not_found` branch.
- `web/ui/pages/plugins_store.html` — **a real bug found during testing,
  not part of the original plan**: this page's own inline `<script>` has
  a page-local `T` lookup object (the project's mandated pattern for
  i18n in inline JS), and the client JS falls back to `T[j.error] ||
  j.error` — a key missing from `T` displays the *raw locale-key string*
  to the shop owner. The new server-side key wasn't in this page's `T`
  object, so without this fix a stale/removed listing would have shown
  `"plugins.install.error.not_found"` verbatim instead of a translated
  sentence. Added the missing entry.

## Independent review

Fresh-context Sonnet subagent, `isolation: "worktree"`, reviewed the diff
cold with no visibility into the implementation reasoning. Findings:

- **No blockers.** Verdict: safe to merge.
- Checked and confirmed **no bug** in branch ordering between the
  `not_entitled` and `listing_not_found` checks in
  `plugins_store_page.go` — both test mutually exclusive `Code` string
  values on the same `*marketplace.APIError`, so order doesn't matter.
- Checked and confirmed **no envelope-shape confusion** — verified the
  cloud's structured `{"error":{"code":...}}` (read via
  `marketplace.APIError`) and the till's own bare-string
  `{"error":"plugins.install.error.not_found"}` (written by `respond()`)
  are two distinct layers and the diff doesn't mix them.
- Swept every `not_entitled` call site in the repo and confirmed the two
  changed spots (`ClassifyInstallError`, `plugins_store_page.go`'s inline
  check) are the only two that needed a parallel `listing_not_found`
  case — `ClassifyInstallError` itself fans out to all 3 real call sites
  (`plugin_api.go`'s Update button ×2, `cloudsync_wire.go`'s replica
  sync), so no site was missed.
- Independently verified the claim that `plugin_install_modal.html`
  already had `plugins.install.error.not_found` wired (a different flow,
  pre-existing) — confirmed true by reading the file directly.
- Judged the `web/help/` topic does **not** need an update: the existing
  `web/help/en/plugins.md` doc already describes that a blocked download
  "shows a message explaining that" without enumerating every possible
  message string; this is a correction to *which* message appears on an
  existing, already-documented failure path, not a new screen/control/
  step. Noted as a defensible judgment call, not a gap.

## Verified beyond automated tests

- **TDD re-verified independently** (not taken on the implementer's
  word): the reviewer reverted the `install_status.go` fix and the
  `plugins_store.html` fix separately, re-ran
  `TestClassifyInstallError_ListingNotFound` and
  `TestPluginStoreJSErrorLookupCoversServerErrorKeys`, confirmed both
  failed with the exact claimed error messages, then restored both and
  confirmed green again.
- Full `go test ./...` — all packages pass, zero failures.
- `go build ./...`, `go vet ./...`, `gofmt -l` on all 6 changed files —
  clean.
- `bash scripts/ci/guard-data-access.sh`,
  `bash scripts/ci/guard-kiosk-engine.sh`,
  `bash scripts/ci/guard-plugin-menu-read.sh`,
  `bash scripts/ci/guard-i18n.sh`, `bash scripts/ci/guard-help-topics.sh`
  — all pass.
- No real client/shop name, no hardcoded secret, anywhere in the diff.
- Did **not** drive this in a live browser: the change is a JS
  string-lookup addition with no layout/markup/CSS change, so the
  template-render test (`TestPluginStoreJSErrorLookupCoversServerErrorKeys`,
  which renders the real page and asserts the compiled inline script
  contains the correct key mapping) covers the actual risk here. Noted
  explicitly as a real-but-accepted scope decision, not an oversight.

## Explicitly out of scope

- ut-docs#704 (a separate, adjacent REST-mapping gap for
  `ErrRegisteredRequired`) — different card, not touched here.
- The catalog cache-staleness mechanism that produces a stale listing ID
  in the first place — this fix only corrects the message shown when it
  happens.

## Verdict

Safe to merge. No ADR needed (mirrors the existing
`ErrReleaseNotFound`/`ErrReleaseUnavailable` pattern exactly). No new
i18n key needed.
