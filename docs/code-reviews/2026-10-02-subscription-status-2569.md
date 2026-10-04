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

## Delta review 2026-10-04: merge of main + ADR-0148 interaction

**What changed since the first review:**
- `origin/main` merged in (`32cb6963c`). Conflicts were additive and resolved as unions: chip allowlists in `internal/auth/middleware.go` and `internal/pages/demo_mode.go`, status-bar CSS, and the `claim` help topic in 5 languages. Screenshots were regenerated (`df8c740b0`).
- **Semantic clash with ADR-0148 (ut-docs#3615, merged on main meanwhile):** a lapsed till no longer checks in by itself, so "renew it in your account" alone left it stuck.
  - `61bf0edde`: the lapsed line and the help now point at Check for a paid plan.
  - On a registered till with cloud sync off, the #registration banner leaves cloud sync out, because main's cloud-sync-off notice already says it.

**Reviewer:** independent model (Fable; the author was Opus 5.5), 2026-10-04. It ran build, vet, the pages/auth/entitlement tests and the i18n/help-drift guards, and checked the merge with `--remerge-diff`: no dropped lines, no duplicate map or JSON keys.

**Findings and outcome:**
1. *Minor, fixed (`b66b86682`).* A replica registered through its main till receives the lapsed state by relay, but has no Check for a paid plan button. A new key, `subscription.banner.lapsed.fix_via_main`, now points at the main till, in 4 core locales and 3 packs.
   - TDD: the new replica subtest failed first, then passed.
2. *Nit, fixed.* The help said the button is how a till learns of a renewal. It now says the button is the *quickest* way; registering, pairing, installing a plugin or generating a claim code also work.
3. *Nit, accepted.* An empty or unknown `subscription_status` reads stale while `SyncAllowed` is false. This is unreachable in practice, because `Block.Values`/`RelayValues` reject an unknown status.
4. *Nit, out of scope (main's text).* `settings.enrol.cloud_sync_off` says "syncs automatically". It sits right beside the button, so it is harmless.

**TDD re-verified by the reviewer** in its own clone: with the template and en text of `61bf0edde` reverted, the lapsed subtest failed with both expected messages and the stale subtest passed; after restoring, both passed.

**Checks (2026-10-04):**
- Full `go test ./...` on the merge in WSL with Go 1.27.1: all green. `internal/plugins` needs `-timeout 30m` locally.
- After the fixes: `pages`, `auth` and `entitlement` green; guards i18n, help-drift, help-topics, compliance-claims and docs-shots pass.
- Pack keys equal core `en.json` (de 1.1.183, es 1.1.173, pt 1.0.30).

**Verdict:** safe to merge.
