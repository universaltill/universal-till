# Review: v0.30.20 release notes (en/de/tr/fa/ar)

- **What:** the owner-facing "What's new" note for v0.30.20, covering the
  cards merged since v0.30.19:
  - ut-docs#3383 and #3652, stock locations on my.;
  - #3673, the unpaid till's start-up check-ins;
  - #3653, promotions and kitchen stations at phone width;
  - #3461, "Paid until" on the Subscription card.
- **Left out:**
  - #3637 is not owner-visible.
  - #2905 is test-only.
  - #3466 is the till half of kiosk unlock. Owners can't use it until the
    my. button ships, and the card is still open for a device check.
- **Drafted by:** Opus 5.5 (lane:cloud-54).
- **Reviewed by:** Sonnet 5, a different model.

## Findings (all terminology; content matched en in every locale)

All fixed:
- **de:** "stilllegen" → "deaktivieren", matching help/de.
- **tr:**
  - "kullanımdan kaldır" → "devre dışı bırak", matching tr.json;
  - German-style quotes → Turkish quotes.
- **fa:**
  - "پلن"/"اشتراک پولی" → "طرح", matching fa.json;
  - "بازنشسته" → "غیرفعال".
- **ar:**
  - "الكاش" → "الصندوق" (and "الصندوق الأساسي" for the main till), with
    gender agreement fixed;
  - "باقة" → "خطة";
  - "موقوف" → "غير نشط".

## Checks

- `guard-release-notes.sh v0.30.20`: ok.
- `guard-compliance-claims.sh`: ok.
- `guard-competitor-naming.sh`: ok.
- `go test ./internal/releasenotes/`: ok.

## Open item

The German "Bezahlt bis" for the Subscription card label is unverified,
because the de pack is not checked out here.

## Verdict

Safe to merge.
