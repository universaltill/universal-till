# Code review: extend the fiscal hard gate to Turkey (ut-docs#1208)

**Branch:** `feat/1208-tr-fiscal-hard-gate` · **PR:** (opened alongside this record)
**Reviewer:** independent Opus subagent, fresh context, own read/build/test pass on this checkout
**Author:** Sonnet (this pipeline cycle)

## What shipped

`internal/fiscal.RequiresHardGate` returned `true` only for `"DE"`.
`reference/turkey-compliance.md` §1 (ut-docs#1158's Turkey research pass)
flagged the gap directly: Turkey's Law No. 3100 requires a GİB-certified YN
ÖKC fiscal device the same way Germany's KassenSichV requires a TSE, and
until the gate covers `"TR"` too, a Turkish till declaring
`fiscal.system_of_record` is not gated at all — the still-nonexistent
`ut-plugin-tax-tr`'s absence would silently permit an unsigned sale instead
of failing closed. Per ADR-0050 Decision 2, the enforcement point that
notices a plugin's absence has to be core's, not the absent plugin's — the
same reasoning that already gates DE while `ut-plugin-tax-de` is itself an
incomplete skeleton.

Fix: `RequiresHardGate` now returns `true` for `"DE"` and `"TR"`.
`EvaluateGate` is otherwise untouched — a TR shop that hasn't declared
`fiscal.system_of_record` is fully unaffected (still `Allowed`, no settings
read at all), and one that has, with no signer configured, now hits
`BlockedNeverConfigured` exactly like Germany.

**Card scope was narrowed, not fully done.** #1208 also asked for a specific
GİB-approved device/vendor and a working `ut-plugin-tax-tr`; that needs a
certified device or vendor SDK access this pipeline doesn't have, so it's
split off to ut-docs#1280 with a light vendor/protocol research note
(`reference/turkey-compliance.md` §1a) rather than left as unfinished scope
on this card. This record covers only the core-gate slice actually shipped.

## Independent review — verdict on first pass: one real finding, fixed; now safe

Full independent pass (different model, fresh context), including its own
call-site sweep (not trusting this diff's own account of what it touched)
and its own mutation-based TDD re-verification.

**Verified correct and left unchanged:**
- `EvaluateGate`'s branching — non-gated country short-circuits before any
  settings read; `system_of_record=false` stays `Allowed` regardless of
  country; a TR shop with `system_of_record=true` and no signer hits
  `BlockedNeverConfigured`.
- Full call-site sweep of `RequiresHardGate` (`fiscal.go` itself,
  `menu_page.go`, `fiscal_signer_banner.go`) confirmed by independent grep,
  not just the sites this diff's own comments named.
  `fiscal_signer_banner.go` is generically correct as-is (resolves the
  `fiscal.sign.ask` hook owner dynamically, copy already says "the
  country's tax plugin" not "TSE") — its stale "today: DE" doc comment is
  cosmetic, not fixed here to keep the diff to what it needed.
  `setup_tse.go` doesn't call `RequiresHardGate` at all (its own
  `tseProvisionCountry = "DE"` constant scopes it) — not a missed call site.
- Test quality, verified by mutation rather than trusted: reverting
  `menu_page.go`'s fix in isolation fails exactly
  `TestMenuPage_FiscalRegisterTileHiddenForTurkeyEvenWithGermanPluginActive`
  and nothing else; reverting `RequiresHardGate` to `country == "DE"` fails
  exactly `TestRequiresHardGate` and
  `TestEvaluateGate_TurkeySystemOfRecordWithoutSignerIsHardBlocked`. Both
  reverts were restored and the checkout left clean afterward.
- Scope: minimal, no unrelated changes; the `menu_page.go` import removal is
  a necessary consequence of its own fix, not scope creep.

**Blocker found and fixed — B1: Germany-specific refusal copy was reachable
for a blocked Turkish shop.** `pos.toast.fiscal_never_configured` /
`fiscal_tse_failing` and their `refund.error.*` twins (`en`/`tr`/`ar`/`fa`)
hardcoded "this shop is set to record real sales **for Germany**" and "no
**TSE (technical security device)**". `tr.json` translated this faithfully
into Turkish, so the one message a blocked TR cashier actually sees would
have told them, in Turkish, that their shop was configured for Germany.
Reachable from `pos_api.go`, `refund_page.go`, `shifts_api.go`,
`inventory_api.go` — i.e. every real tender/refund/payout path this gate
now covers for TR. Fixed: all four keys, across all four locales, reworded
to name "its country's legal system of record" / "fiscal-signing device"
generically, matching the wording precedent already established by
`settings.fiscal_signer.missing.message` ("no fiscal-signing plugin is
installed" — that string is already country-generic today). The internal
`fiscalNeverConfiguredError`/`fiscalTSEFailingError` sentinel strings and
their doc comments (`pos_api.go`) were generalized the same way, since they
also named "Germany"/"TSE" specifically and are the raw text the localized
copy exists to keep from leaking (ut-docs#731 finding B1's defect class).

Five tests asserted on the now-removed literal substrings ("TSE", "no TSE",
"technical security device") and were updated to assert on "fiscal-signing
device" instead — in the four `…BlockedWhenTSENeverConfigured` tests that
also check a response-specific prefix ("Refund not completed"), that prefix
is what actually distinguishes the localized copy from a raw-sentinel leak
(the raw sentinel never carries it), not the TSE/device wording, so the
distinguishing power those tests were written for is unchanged — confirmed
by re-running the mutation checks above after the wording change.

**Should-fix, found and fixed — S1: `README.md`'s pricing/compliance FAQ
claimed enforcement was Germany-only.** "The till enforces this for
Germany: …" is now stale. Reworded to "on a per-market basis" without
naming Turkey specifically — Turkey is not a shipped or announced market
yet (shadow-mode-only per `reference/turkey-compliance.md` §8, and #130's
roadmap card is unstarted), so asserting the till "enforces this for
Turkey" in customer-facing copy would overclaim market readiness that
doesn't exist; the per-market phrasing is accurate today and needs no
further edit as more markets are added. Left the "e.g. Germany's TSE"
example and the Free-tier bring-your-own-device pricing claims untouched —
this is a mechanism-scope correction, not a pricing decision, and
`guard-compliance-claims.sh` stays green after the edit.

**Deferred — real, out of scope for this card.** The "configured and
healthy/failing" cosmetic layer (the settings-page fiscal chip
`fiscal.chip_ok`/`fiscal.chip_degraded`, the override-active banner
`fiscal.banner.override_active`, and the receipt's `receipt.fiscal.tse.*`
serial-number/certificate-ID fields) still says "TSE" throughout and is
reachable only once a shop has a signer actually configured for its
market — for TR that requires a human to first manually set
`fiscal.tse_configured=true` with no real `ut-plugin-tax-tr` behind it, a
contrived state, not the realistic path this card's gate protects against
(a shop that never configured anything). Generalizing that layer is a
genuine per-country UI/data design question — what does a "signed receipt"
even display for a market whose signing mechanism isn't a TSE — not a
one-line wording fix, and belongs with the actual `ut-plugin-tax-tr` build
(ut-docs#1280) or its own card, not bundled into widening the hard gate.
Not fixed here on purpose; flagged so the next sweep doesn't have to
re-find it.

## Commands run (this checkout, post-fix)

- `gofmt -l .` — clean. `go build ./...` — clean. `go vet ./...` — clean.
- `go test ./...` (whole module) — pass, no failures in any package.
- `scripts/ci/guard-compliance-claims.sh`, `scripts/ci/guard-i18n.sh`,
  `scripts/ci/guard-plugin-menu-read.sh` — all pass, run individually.

## TDD / regression re-verification (performed personally, not taken on trust)

1. **`menu_page.go`'s `country == "DE"` check reverted to
   `fiscal.RequiresHardGate(...)`:** exactly
   `TestMenuPage_FiscalRegisterTileHiddenForTurkeyEvenWithGermanPluginActive`
   failed (rendered the German §146a Abs. 4 AO fiscal-register tile for a
   TR shop with the German plugin active); every other menu test stayed
   green. Restored → green.
2. **`RequiresHardGate` reverted to `country == "DE"`:** exactly
   `TestRequiresHardGate` and
   `TestEvaluateGate_TurkeySystemOfRecordWithoutSignerIsHardBlocked` failed.
   Restored → green.
3. Both reverts performed on a copy of the file and diffed back to the
   fixed version rather than trusted from memory; `git status` after
   restoring showed no stray changes.

## Safe-to-merge verdict

**Yes**, after B1 and S1 are fixed and re-verified above. No remaining
blockers. The deferred cosmetic-copy item is real but out of scope and
tracked for whoever picks up ut-docs#1280.
