# Review — release notes v0.32.0

**Shipped:** `web/release-notes/{en,de,tr,ar,fa}/v0.32.0.md`. New: gift voucher
item type (#4214), Plugin Store tiles + detail page (#4180). Improved: AI
assistant on a joined till refuses catalogue changes up front (#4168). Fixed:
phone item-tile word breaks (#4175), plugin settings sideways scroll on phones
(#4098). Not credited: the country check-in (#4020, groundwork for my., not
shipped there, per RELEASING.md step 1); test-flake and note-correction PRs
(internal). Minor version because of the gift voucher feature.

**Reviewer:** independent Sonnet subagent (written by Opus 5.5). It found the
notes accurate and not overclaimed. Fixed: tr headline "satın" → "satabilirsiniz",
"çek" → "hediye çeki", app's label "Paket sipariş vergi oranı istisnaları"; ar
"الجهاز" for the till and the app's "تجاوزات معدل الضريبة للطلبات الخارجية"; fa
"فروشگاه افزونه‌ها" and formal phrasing; de "Steuersätze außer Haus". The old
"Sell a voucher" label is quoted from git history in each language.

**Verified:** `scripts/ci/guard-release-notes.sh v0.32.0` ok.

**Verdict:** safe to merge.
