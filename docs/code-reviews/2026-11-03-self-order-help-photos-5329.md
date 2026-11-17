# 2026-11-03 — self-order help: cart item photos in de/ar/fa/tr (ut-docs#5329)

**What shipped.** universal-till#2272 (ut-docs#4959) added one sentence to
`web/help/en/self-order.md`: each self-order cart line shows the item's photo
or icon (the same picture as its tile), and an open cart picks up a changed
picture on its next update. This change carries that sentence into
`web/help/{de,ar,fa,tr}/self-order.md`, appended to the same (dine-in/takeaway
cart) paragraph. Help prose only; no code, no locale keys, no screenshots (no
docs shot covers a self-order page).

**Pre-flight:** web/help topic — updated (de, ar, fa, tr); `make docs-shots` —
n/a (no self-order screenshot; text-only); reference/ page — n/a (no behaviour
change); README — n/a; en.json keys — n/a (none added); help-drift baseline —
n/a (no entry records this gap; structure unchanged); review record — this file.

**Review** (independent subagent, different model from Dev): approve.
- minor (tr) — the first wording left the possessive and "bunu" without a
  clear antecedent. **Fixed**: split into two sentences ("…fotoğrafını veya
  simgesini gösterir; bu görsel, ürünün kutucuğundakiyle aynıdır. Görseli ürün
  düzenleyicide değiştirirseniz…").
- nit (ar) — "بطاقته" for the item tile; the ar catalog/display topics use
  "مربع" for an item tile. **Fixed** → "مربعه".
- nit (de) — "Artikeleditor" vs "Artikel-Editor" in de/catalog.md; the locale
  already mixes both (categories.md uses each once). **Accepted**: Duden
  spelling; no change.
- Meaning checked against en (photo OR icon; same picture as the tile; edited
  in the item editor; an OPEN cart shows it on its NEXT update); register
  matches each file; no bidi issues in ar/fa.

**Verification.** Guards help-drift, help-topics, help-terms,
help-breadcrumbs, compliance-claims, competitor-naming, i18n: exit 0
(re-run after the fixes); rtl-logical-css exit 0 (Tester). `go build ./...`;
`go test ./internal/manual/...` and `./internal/pages -run Help` pass.
Tester drove a real till (isolated data dir): `GET /help/self-order?lang=
de|ar|fa|tr` → 200, the sentence renders once at the end of the cart
paragraph, `dir=rtl` on ar/fa and `ltr` on de/tr.

**Process note.** The branch keeps a `WIP: pre-review snapshot` commit: the
session could not amend + force-push it. The PR merges with a merge commit, so
the first-parent history on `main` carries the PR title.

**Verdict:** merge. **Deferred:** none.
