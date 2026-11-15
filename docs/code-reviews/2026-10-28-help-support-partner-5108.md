# Review: help topic "Support partner" with a link to my.'s screen (ut-docs#5108)

**Date:** 2026-10-28 · **Branch:** `feat/5108-help-support-partner` · **Lane:** `lane:cloud-41`
**Author model:** Sonnet (Dev subagent) · **Reviewer:** Opus 5.5, fresh context (different model)
**Companion:** ut-cloud#703 (public `GET /partner` redirect to my.), lands first.

## What shipped
- New help topic `support-partner` (section "Connecting & extending", order 362) in en/de/tr/fa/ar:
  what a support partner is, where the owner connects/disconnects it on my.universaltill.com
  (your shop → Support partner, right after Shop profile), what the partner sees and does not see
  (same facts as my.'s legal-reviewed consent text `partner-connect-v2`), and that selling — offline
  included — is never affected.
- `enroll.PartnerURL` (next to `PlansURL`): `<cloud web base>/partner?store=…&lang=…`; ut-cloud 302s
  it to my.'s `/<locale>/stores/<id>/partner`. The till never hard-codes my.'s host.
- `help_page.go`: only for that topic, when the resolved marketplace config is complete
  (`CredentialsComplete(m)` on one `Effective` copy) it adds the URL and a QR (same pattern as the plans
  handoff). Pure string/QR work — no network call; an unregistered till, or a replica registered only
  through its main till, gets the prose only.
- `help_topic.html`: caption + QR (`alt=""`, the caption already says it) + URL link
  (`target=_blank rel=noopener hx-boost=false`, like the other external links).
- New key `help.support_partner.scan` (+ meta) in en/ar/fa/tr; pack follow-ups de/es/pt in the same cycle.

## Findings
| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | Turkish caption/summary used "Dükkânı yönet"/"Destek ortağı"; my. says "Mağazayı yönet"/"Destek iş ortağı". | Fixed (title, path, caption, summary). |
| 2 | Medium | "What the partner sees" omitted "(including a trial end date or a missed payment)" — the item the #4805 legal review (H1, L4) flagged — and the commission line. | Fixed in all 5 languages, reusing ut-cloud's legal-reviewed `manage_app.partner.consent` wording. |
| 3 | Low | No test covered the credential guard (endpoint + store but no token still showed nothing only by accident). | Fixed: `TestHelpSupportPartnerNoLinkWithoutCredential`; verified it fails with the guard removed. |
| 4 | Low | `HasCredentials(cfg)` then a second `Effective(cfg)` — `CredentialsComplete`'s doc says guard on the resolved copy. | Fixed. |
| 5 | Low | Replica tills (registered via the main till) get prose only while the prose said "if this till is registered". | Fails closed (accepted); prose now says "registered with the cloud itself (not only through the main till)". |
| 6 | Low | Language packs lack the new key. | Pack PRs de/es/pt in this cycle. |
| nit | — | QR alt duplicated the caption; `image-rendering: pixelated` missing; "list" vs my.'s "tree". | Fixed (alt="", pixelated, en "tree"). |

Found by the orchestrator before review: the first draft said the path was "your shop → Manage shop →
Support partner" (my. *is* Manage shop) and claimed the partner could receive reports; both corrected
against `ut-my-shop/help/en/partner.md`.

Checked fine (reviewer): URL/QR escaping (`template.URL` only over a fixed `data:image/png` prefix and our
encoder's bytes, as in the subscription panel), no enforced CSP on till pages, Android opens off-origin
links in the system browser and not under Lock Task, logical CSS only, frontmatter section names per locale.

## TDD re-verification
- Reviewer (isolated worktree): partnerURL block removed → `TestHelpSupportPartnerLinkWhenRegistered`
  fails (URL, QR, link, data URI missing; full page and fragment).
- Orchestrator: guard replaced by a store/endpoint-only check → the new no-credential test fails;
  restored → passes.

## Driven run (Tester)
Built binary, throwaway data dir, `UT_MARKETPLACE_*` pinned to a test identity (`test-5108-shop`),
headless Chromium on `/help/support-partner`: block renders with QR and
`…/partner?lang=en-gb&store=test-5108-shop` at 1024×600 (en), 360×740 (en; de), and RTL fa at 1024×600
(`dir=rtl`, `lang=fa-ir`); no horizontal scroll. Looked at the screenshots. Not checked: a real phone
scanning the QR against production (needs ut-cloud#703 deployed), native-speaker review of tr/fa/ar.

## Gate
gofmt clean, `go build ./...`, golangci-lint 0 issues, every guard in ci.yml's build job, `go test ./...`.

## Verdict
Safe to merge after ut-cloud#703.
