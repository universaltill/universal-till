# 2026-11-03 — catalog help: Android "Take a Photo" sentence in ar/fa/tr (ut-docs#5328)

**What shipped.** Commit f948cda added one sentence to `web/help/en/catalog.md`
and `de/catalog.md` (the **Item image** bullet): in the Android till app,
**Take a Photo** opens the tablet's own camera app (focus, flash, a proper
photo) and the photo is saved as soon as you accept it. This change carries
that sentence into `web/help/{ar,fa,tr}/catalog.md`, at the same place (after
the live-preview / Choose File sentence, before the iPhone/iPad sentence).
Help prose only; no code, no locale keys, no screenshots.

**Pre-flight:** web/help topic — updated (ar, fa, tr; en/de already in
f948cda); `make docs-shots` — n/a (text-only, no screenshot shows the Android
camera hand-off); reference/ page — n/a (no behaviour change in this diff);
README — n/a; en.json keys — n/a (none added); platform claim (README /
ut-website) — n/a (no new claim, a help sentence for existing behaviour);
help-drift baseline — n/a (structure unchanged); review record — this file.

**Review** (independent, Opus 5.5; Dev was Sonnet): approve.
- Label match: the sentence uses each locale's `catalog.take_photo` —
  ar «التقاط صورة», fa «گرفتن عکس», tr **Fotoğraf Çek** — with the same
  quoting/bold convention the same bullet already uses for that label.
- Terms consistent with each file: ar «الصندوق», fa «صندوق», tr «kasa» for
  the till; "Android till app" phrasing matches existing "تطبيق Android" /
  "برنامهٔ Android" / "Android uygulaması" uses.
- nit (ar, fa) — "a proper photo" was rendered as "a really good photo"
  (ar «صورة جيدة حقًا», fa «یک عکس واقعاً خوب»), a mild overclaim; the point
  is a full camera capture vs. a preview still. **Fixed** → ar «صورة بجودة
  كاملة», fa «عکسی با کیفیت کامل» ("a full-quality photo"). tr «gerçek bir
  fotoğraf» kept (matches de "ein richtiges Foto").
- Meaning checked against en (Android till app only; opens the tablet's own
  camera app *instead*; saved on accept); no bidi issues in ar/fa; nothing
  else in the three files changed (`git diff --stat`: 1 line each).

**Verification.** Guards help-drift, help-terms, help-topics,
compliance-claims, competitor-naming, rtl-logical-css, mail-addresses: exit 0
(re-run after the fix).

**Verdict:** merge. **Deferred:** none.
