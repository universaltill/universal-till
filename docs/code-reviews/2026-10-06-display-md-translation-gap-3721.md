# Review: display.md item 16 translation gap (ut-docs#3721)

**Card:** ut-docs#3721 — DE/AR/FA/TR never got the "parked orders / other-till
sync" sentence that #3253 added to the EN version of item 16 (Settings →
Data → Erase a customer, GDPR). Pre-existing drift, not introduced by #3435.

## What shipped

Translated the missing EN sentence into DE, AR, FA and TR and inserted it at
the same place in each locale's `web/help/<locale>/display.md` item 16 —
right after "...only the link to that customer's identity." and right
before "Plugins that keep their own copy of customers...":

- DE: "Auch geparkte Bestellungen verlieren den Namen des Kunden, und jede
  andere Kasse im Geschäft entfernt die Kundendaten bei ihrer nächsten
  Synchronisierung mit der Hauptkasse."
- AR: "تفقد الطلبات المعلّقة اسم العميل أيضًا، ويحذف كل جهاز آخر في المتجر
  بيانات العميل عند مزامنته التالية مع الجهاز الرئيسي."
- FA: "سفارش‌های نگه‌داشته‌شده نیز نام مشتری را از دست می‌دهند، و هر صندوق
  دیگری در فروشگاه اطلاعات مشتری را در همگام‌سازی بعدی خود با صندوق اصلی
  حذف می‌کند."
- TR: "Bekletilen siparişler de müşterinin adını kaybeder ve mağazadaki diğer
  her kasa, ana kasayla bir sonraki eşitlemesinde müşteri bilgilerini siler."

Diff: exactly 4 files, 1 line each. EN and all other locales/items untouched.

## Terminology sourced from the existing files, not invented

Each locale's wording for "till", "main till", "parked order" and "shop" was
pulled from the SAME paragraph where possible, else the same locale's
`open-orders.md`/`users.md`/`sell.md`/`tables.md`/`catalog.md`/`multitill.md`
(e.g. AR already uses "جهاز" rather than "صندوق" within this exact
paragraph, so the new sentence follows that rather than the other AR files'
"صندوق").

## Review

Independent review by a different model (author: Sonnet; reviewer: Opus
5.5, fresh context, isolated worktree) per `scrum-master/MODEL-ROUTING.md`'s
`complexity:easy` row. Reviewer re-verified terminology by grepping each
locale's other help files directly, rendered item 16 of all 4 locales (plus
EN as a control) through goldmark with `extension.GFM` — the same
configuration as `internal/manual/manual.go:85` — and confirmed each
produces one clean `<li>` with no broken tags, no leftover English, no
stray markdown control characters. Confirmed the diff scope is exactly the
4 expected files/lines. Verdict: **safe to merge**, no blocking findings.

Two non-blocking notes, both accepted as-is:
1. FA/TR item 15 use a different word for "parked" ("پارک‌شده" /
   "park edilmiş") than the new item-16 sentence
   ("نگه‌داشته‌شده" / "Bekletilen") — pre-existing inconsistency, out of this
   card's scope; the new sentence correctly matches each locale's
   `open-orders.md`/`tables.md` vocabulary instead. A possible separate
   follow-up if item 15's wording should be normalized, not filed here since
   it's cosmetic and pre-existing.
2. FA's comma-before-"و" in the new sentence matches the same paragraph's
   existing style — no change needed.

## Verified beyond automated tests

- `bash scripts/ci/guard-help-drift.sh` — exit 0, no new drift reported for
  `display.md` in any locale (structural counts unchanged, as expected for
  a same-length sentence insertion).
- `bash scripts/ci/guard-help-topics.sh` — exit 0.
- `bash scripts/ci/guard-compliance-claims.sh` — exit 0 (359 files scanned).
- `bash scripts/ci/guard-competitor-naming.sh` — exit 0 (361 files scanned).
- `go build ./...`, `go vet ./...` — clean.
- `go test ./internal/manual/...`, `go test ./internal/pages/...` — ok.
- Rendered each locale's item 16 through goldmark (same config the app
  uses) — reviewer did this independently in addition to the author; both
  got a single well-formed `<li>` per locale.
- No screenshot/driven-app run: this is a prose-only addition to an
  existing, already-rendering help item (no new markdown syntax, no layout
  change, no new interactive surface) — verified instead via the structural
  drift guard plus the goldmark render check above.

## Safe-to-merge verdict

Yes.
