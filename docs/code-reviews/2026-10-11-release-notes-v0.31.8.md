# Review: release notes v0.31.8 (ut-docs#4094)

**What shipped:** the owner-facing note for v0.31.8 in en/de/ar/fa/tr. Its only entry is the catalog.photos.v1 fix (identify by camera now uses the photographed items first; needs AI Assistant 2.2+). It is the only change merged since v0.31.7.

**Review:** checked by the cycle's own model, not by a separate model: the content is a direct restatement of the reviewed change (`2026-10-11-catalog-photos-view-4094.md`).
- `guard-release-notes.sh v0.31.8` passes.
- The headings match each locale's earlier notes (`Behoben`, `Düzeltildi`, `إصلاحات`, `رفع اشکال‌ها`).
- Product names match the existing locale terms: KI-Assistent, Yapay Zekâ Asistanı, المساعد الذكي.
- No PR or card numbers in the notes, and no real shop names. The example item name is generic.

**Verdict:** safe to merge.
