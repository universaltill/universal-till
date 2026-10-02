# Review: subscription status chip, Settings card and paused banners (ut-docs#2569)

**Change:** ADR-0060 §6, the merchant-facing half of the entitlement model.
- `entitlement.Describe` derives one of four states (free / active / stale / lapsed) and the paused capabilities, reusing `EffectivePlan`.
- `GET /ui/subscription-chip` adds a status-bar chip, for settings-permitted viewers and only in the stale/lapsed states.
- A Settings → Subscription card shows the plan, status, renewal date (display only) and last confirmation.
- A shared paused banner appears in that card, in the registration card (cloud sync / browser catalogue) and in the TSE provisioning block (managed TSE, plus "an existing TSE keeps signing").
- 30 locale keys (en/ar/fa/tr) and a help section in `claim`. Nothing is disabled: the change explains, it does not gate. Nothing on the sale path reads it (`salepath_imports_test.go`).

**Reviewer:** independent model (Fable; the author was Opus 5.5), 2026-10-02.

**Checked and fine:**
- The settings page never 500s for a free or unenrolled till.
- The chip, card and status-bar mount share one permission gate (`settings`); a cashier or no-session request gets an empty 200.
- The route uses normal auth and is listed as a background poll and a shell GET; the demo allow is a KV read.
- ADR-0060 §5–§7 hold: no sale-path read, `ExpiresAt` is never compared to now, no modal, and it degrades in place.
- Banner filtering is correct; CSS uses logical properties.
- en/ar/fa/tr are complete and keep every `%s`.

**Findings and outcome:**
1. *Should-fix, fixed.* `last_confirmed_at` present but empty, which a replica can copy: the till read **stale** and claimed "couldn't confirm for more than 7 days" about a till that was never confirmed. An empty value now counts as never confirmed, which reads free. TDD: the new table case failed first (`Describe = stale`), then passed.
2. *Should-fix, accepted as is.* A clock skew beyond Grace, or an unparsable timestamp, also reads stale with the "more than 7 days" wording. That is rare and self-healing on the next check-in, so the wording is kept.
3. *Nit, fixed.* The chip is a link with a tiny tap target. It now has `min-block-size: 2.25rem`, like other status-bar controls.
4. *Nit, fixed (pack).* The de pack said "Local (kostenlos)" while the de help said "Lokal (kostenlos)"; the pack now says Lokal.
5. *Nit, deferred.* "Renews %s" is wrong for a subscription cancelled at period end. "Paid until %s" would be safer, but it is a copy change in four locales plus three packs; filed as a follow-up.

**Language packs (de/es/pt, 30 keys each):** meaning, register (Sie / usted / pt-PT), `%s` and no certification claims were all checked; no other findings.

**Checks:**
- Dev: full `go test ./...` in WSL (78 packages ok); gofmt/vet clean; guards i18n, compliance-claims, help-topics, help-drift, data-access and core-neutral all pass.
- The new tests fail against deliberately broken code (gate removed, filter removed).
- docs-shots: surface hash refreshed (PNGs not committed: font noise only).
