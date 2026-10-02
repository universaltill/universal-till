# Review: release notes v0.30.16, iOS Bluetooth line (ut-docs#3261)

- Date: 2026-10-02. Author lane: lane:cloud-24 (Opus 5.5).
- Scope: one *Fixed* bullet appended to `web/release-notes/{en,de,tr,fa,ar}/v0.30.16.md`, for the change merged in #1608.
- Each translation reuses the terms already reviewed by Fable for #1608's locale keys and help bullets:
  - de: Einstellungen-App, Bondrucker und -Waagen;
  - ar: الإعدادات;
  - fa: Settings ← Bluetooth;
  - tr: Ayarlar uygulaması.
- No PR or card numbers, plain *Fixed* wording (`RELEASING.md`). `guard-release-notes.sh v0.30.16` and `guard-compliance-claims.sh` pass.
- Independent review: none separate. The wording is a subset of strings already reviewed in #1608. Accepted as a docs-only, low-risk change.
