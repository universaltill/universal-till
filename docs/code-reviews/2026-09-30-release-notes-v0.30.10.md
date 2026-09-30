# 2026-09-30 — Release notes for v0.30.10

Written by lane:cloud-24 (Opus 5.5) and reviewed by Fable against the nine
first-parent merges since v0.30.9 and their review records. The review was
read-only, and its verdict was safe to publish.

- Applied:
  - F1: the phone bullet now also covers Dine in / Takeaway beside Pay and the
    rounded, sticky category row.
  - F2: the erasure wording now matches the shipped UI sentence, "because tax law
    requires you to keep them". It describes the retention rule and is not a
    certification claim (ADR-0040).
  - F3: the bare first-boot setup bullet is dropped. That route isn't reached from
    the normal UI.
  - F5–F10: terminology nits in de/ar/fa/tr, aligned with web/help and
    web/locales.
- Skipped: F4 (optional; it names the All Settings path).
- Noticed, not fixed here: `web/help/de/display.md` step 9 says "Leitung" where it
  means a manager. It will be a follow-up card.
- Checks run: `guard-release-notes.sh v0.30.10` and the `internal/releasenotes`
  owner-language tests both pass.
