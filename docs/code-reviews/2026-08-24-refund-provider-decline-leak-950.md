# Code review: refund provider-decline raw text leak (ut-docs#950)

## What shipped

ut-docs#950 — flagged by the independent review of ut-docs#944
(ut-docs#924 increment 2, `docs/code-reviews/2026-08-24-http-error-raw-leaks-924-increment2.md`)
as a separate follow-up rather than folded into that sweep, since it's a
different literal shape (`err.Error()` concatenated into a larger string,
not passed alone) and a genuine design call, not a mechanical key swap.

`internal/pages/refund_page.go`'s payment-provider refund gate
(`payment.<key>.refund`, the blocking hook a payment plugin can register
to veto a refund — e.g. "actually send the money back") sent the plugin's
raw error text straight into the HTTP response body, untranslated:

```go
http.Error(w, "provider refund failed for "+method+": "+blocked.Error(), http.StatusPaymentRequired)
```

`blocked` is plugin-originated — whatever text a third-party payment
plugin's refund hook returns (or the event bus's own wrapper text around
it) — so it reached the operator's screen verbatim, in raw English,
unlocalized, regardless of shop locale.

## Design decision (the reason this wasn't mechanical)

The card explicitly flagged this as needing BA/Architect input: should the
plugin's own decline text reach the operator at all, given it may already
be operator-meaningful ("insufficient float", "refund window expired")
rather than generic noise?

Found a direct, already-shipped precedent for this exact question:
ut-docs#921 (F2) made the identical call for the **sibling**
`payment.<key>.authorize` gate in `pos_api.go`'s `completeTender` — a
plugin's raw decline text is never shown to the operator, only logged
server-side, with a generic localized message returned instead
(`paymentDeclinedError` / `pos.toast.payment_declined`). Refund's gate is
architecturally the same shape (a plugin's blocking hook vetoing a
money-moving action) with the same trust boundary (arbitrary third-party
plugin text, uncontrolled language/length/content) — mirrored the
decision rather than re-litigating it. The independent reviewer
(see below) formed their own judgment on this rather than taking the
commit message's word for it, and agreed, noting the fix is if anything
slightly better than the precedent: the authorize path's
`paymentDeclinedError` discards the plugin's real text entirely, while
this fix preserves it server-side via `LogAndLocalizedError`'s logging,
so it stays diagnosable without being exposed.

## Fix

Routed through `common.LogAndLocalizedError` — the same helper already
used at the other four call sites in this handler — logging the real
`blocked` detail server-side and returning a new localized message:

```go
common.LogAndLocalizedError(w, r, http.StatusPaymentRequired, "refund.error.provider_declined", "refund", blocked)
```

`http.StatusPaymentRequired` (402) is unchanged — the type's existing
doc comment on the authorize sibling explains why that status is
deliberate (lets a caller distinguish a decline from a generic 400), and
it already matched here.

New locale key `refund.error.provider_declined` added to all 4
`web/locales/*.json` files (en/ar/fa/tr), positioned alphabetically next
to the existing `refund.error.server`, each a real translation (not a
copy of the English string) mirroring `pos.toast.payment_declined`'s
existing "try again or choose another method" phrasing:

- en: "The payment provider declined this refund — try again or choose another method"
- ar/fa/tr: local-language equivalents, same structure.

## Regression test

`TestPostRefund_ProviderRefundDeclinedShowsLocalizedMessageNotRawError`
(`internal/pages/refund_page_test.go`) mirrors the existing
`TestTenderHandler_DeclinedPaymentShowsLocalizedMessageNotRawError`
pattern for the authorize sibling: seeds a real plugin/catalog/entry/hook/
permission row set, registers a real blocking subscriber on
`payment.demopay.refund` that returns a decline error carrying a
distinctive string, drives the actual handler through `mux.ServeHTTP`
(real plugin bus, real DB, real HTTP stack — not a mock), and asserts the
localized copy appears while neither the raw plugin string nor the old
Go wrapper text ("provider refund failed for...") appears.

**TDD claim independently re-verified**, not taken on trust — both by me
before commit and again by the reviewer afterward: reverted just the
`refund_page.go` hunk back to the raw `http.Error` call, re-ran the new
test alone, confirmed it fails with the exact raw-leak symptom pasted
into the failure message; restored the fix, confirmed it passes again.

## Independent review

Sonnet, fresh context, same working tree (first attempt hit a transient
API connection error mid-run before producing any verdict and was
discarded — relaunched fresh rather than resumed, per the pipeline's
"fresh subagent over resuming" guidance; `complexity:easy` → Sonnet
builds, fresh-context Sonnet reviews per the model-routing rubric).
Verdict: **safe to merge**, no blockers, no should-fix items — a couple
of nits, both non-issues on inspection:

- Confirmed `blocked.Error()` was the *only* place in this handler
  leaking plugin/Go-error text to the response body (traced the other
  nearby `http.Error` calls — they carry only operator/catalog-derived
  data, out of scope and pre-existing).
- Traced `blocked`'s origin through `blockingPaymentEvent` →
  `blockingPaymentEventWithResponse` → `plugins.EventBus.PublishAuthorize`
  and confirmed the fix squelches both the plugin's own text *and* the
  bus's wrapper text around it — broader coverage than "just the plugin
  string."
- Independently judged the design-precedent question (item 2 above)
  rather than trusting the commit message, and agreed with the call.
- Re-ran `bash scripts/ci/guard-i18n.sh` — green (1201 template keys
  resolve, all locales match `en.json`).
- Independently reverted/restored the fix and re-ran the regression test
  in both directions (see TDD verification above) — confirmed the same
  fail/pass behavior.
- Wrote and then deleted a throwaway diagnostic test to confirm the new
  test's plugin-table `INSERT`s match the page-tests' local schema
  (`seedForPages`, distinct from production's `001_init.sql` — no
  `NOT NULL` mismatch); left the working tree clean afterward
  (`git status --short` empty, confirmed).
- Ran `gofmt -l`, `go build ./...`, `go vet ./...`,
  `go test ./internal/pages/...` — all clean.
- Checked `method` (form-supplied, plugin-topic-influenced) for injection
  risk — none: trimmed, validated via `EnsurePaymentMethod` earlier in
  the same handler, unchanged by this diff.
- Checked the server-side log line for any new logging-safety concern —
  none; same `log.Printf` pattern already used four other times in this
  file.

## Verified beyond automated tests

- `gofmt -l internal/pages/refund_page.go internal/pages/refund_page_test.go` — clean.
- `go build ./...` — clean.
- `go vet ./...` — clean.
- `go test $(go list ./... | grep -v '/internal/plugins$')` — full suite
  green, matching CI's actual invocation.
- `go test -timeout 20m ./internal/plugins` — green (run separately,
  matching CI's split).
- All 16 CI-blocking guards from `.github/workflows/ci.yml`'s `build`
  job run locally — all green, including `guard-i18n.sh` (new key
  resolves in all 4 locales) and `guard-docs-shots.sh`/
  `guard-help-topics.sh` (this diff touches no template or rendered
  screen, only `/api/` error-response text and locale JSON — no manual
  topic or screenshot owed).
- No secrets, no real client/shop names in the new test fixture (reuses
  the existing "Demo Pay"/`demopay` fixture already established in
  `pos_api_test.go`'s sibling test).

## Explicitly out of scope (not fixed here)

- The authorize-path precedent (`pos_api.go`) already discards the
  plugin's raw text entirely rather than logging it — arguably a smaller
  regression than this fix's own (this fix logs it; that one doesn't).
  Not touched here since it's already shipped, working-as-designed
  behavior on a different card (#921), not this ticket's scope.
- `ut-plugin-language-{de,es}` packs need the standard follow-up for the
  1 new locale key; `lang-pack-drift` is advisory-only on this PR.

## Safe-to-merge verdict

Safe to merge. No blockers or should-fix findings from independent
review. The design call the card flagged as needing BA/Architect input
was resolved by mirroring an already-shipped, reviewer-endorsed precedent
(ut-docs#921 F2) rather than inventing new policy. All CI-blocking guards
green; full test suite green (matching CI's real invocation split); TDD
claim independently re-verified in both directions by a different model
than the one that wrote the fix.
