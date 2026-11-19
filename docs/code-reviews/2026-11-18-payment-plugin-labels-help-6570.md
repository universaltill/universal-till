# Review: payment plugin labels in the payments help (ut-docs#6570)

Card: universaltill/ut-docs#6570 (complexity:medium; re-sized from easy at Architect because the change spans four repos).

## What shipped (all four repos)
- **ut-plugin-payment-sumup 1.5.3:** the 5 settings that had no label (`sumup_api_key`, `sumup_merchant_code`, `sumup_affiliate_key`, `sumup_app_id`, `sumup_reader_id`) get `label`/`help` keys `payment_sumup.<name>.label/.help` in `locales/en.json`. `src/settings_labels_test.go` pins that every setting and every option resolves. The guard is re-copied from the template.
- **ut-plugin-payment-stripe 1.6.0:**
  - New `locales/en.json` and `label`/`help` on all 3 settings. `scripts/package.sh` now ships `locales/`; before this, the keys would have rendered as raw text on every till. `check-version-bump.sh` counts `locales/*`.
  - `guard-plugin-i18n.sh` and its CI step are added.
  - `stripeAPI.call` returns `(body, status, ok)`. HTTP 401 now declines with the distinct reason `invalid_secret_key`, on the online charge (`src/online.go`), the reader create step and the refund. A 401 during settle/poll stays `outcome_unknown`. Stripe's error message, which echoes the key, is never logged.
- **ut-docs:** in `scripts/templates/guard-plugin-i18n.sh`, a key missing from a non-base locale now prints a note instead of failing, per ADR-0160 §2 ("Code merges with English only"); the till falls back to the plugin's `en.json`. Orphan keys, duplicates, nesting and verb mismatches still fail. The template test is updated. The stale `architecture/plugin-architecture.md` §7 line ("labels are always the raw key") is fixed.
- **universal-till:** `web/help/en/payments.md` names the fields by their new labels, and says a decline reason goes to the till's log file (linking to recovery), not the cashier's screen.

## Review
Independent review by a different model from the author (Dev was Sonnet; Opus reviewed). Verdict: **approve after fixes**. No high or medium findings.
- The money paths were checked. A 401 is acted on only where Stripe has processed nothing yet: create, refund and online charge. A probe test gave a stored attempt plus a 401 on every call; the result was `outcome_unknown` inside the deadline. Every caller and all three fakes were updated.
- **F1 (low, fixed):** the template test's "note printed" grep passed on the `missing:` line alone. It now captures the output and checks the exact header and line with `grep -F`.
- **F2 (low, fixed):** the help said "the till log" with no location. It now links to recovery's *Where the till's log is*. A Stripe 401 also declines refunds. The "hover" wording was dropped, since hover doesn't work on a touchscreen.
- **F3 (low, accepted):** Stripe's wasip1-only `main.go` reading of the envelope `status` has no unit test. The host field was confirmed at `universal-till/internal/plugins/wasm_hostfns.go:444`, and the wasip1 build and vet pass.
- **F4 (low, accepted):** a 90-char comment line in the template. The file already has many longer lines, and there is no line-length rule.
- **F5 (low, deferred → ut-docs#6629):** other plugin repos' copies of the guard still fail on a missing translation. This is a follow-up card, ut-docs#6629.

## Verification
- **Tester** (separate agent): every new test went red with its fix reverted and green with it restored. Gates were green in both plugins: go test, vet (host and wasip1), gofmt, wasm build, validate, guard-plugin-i18n, package, and version-bump on the real diff. The Stripe tarball lists `locales/en.json`.
- **Driven run:** a `-tags devplugins` till build, with both locally built bundles imported through `/api/plugins/import-from-file`. The settings pages showed English labels and help in Playwright, with no raw keys. On a German till, SumUp's currency label stayed German and the new keys fell back to English.
- **Not driven:** a real Stripe 401, because there is no network to api.stripe.com.
- **Reviewer:** re-ran all gates and re-verified the TDD claims in detached worktrees.

Pre-flight: till user-visible behaviour → `web/help/en/payments.md` updated; docs-shots n/a (no payments screenshot shows plugin settings fields); `reference/` → `architecture/plugin-architecture.md` §7 updated; README updated in both plugin repos; new `en.json` key n/a for core (plugin locales only, English only per ADR-0160); manifest field n/a (`label`/`help` already exist, ut-docs#2832); follow-up → ut-docs#6629 (F5).
