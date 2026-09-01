# Till Designer contextual help topic (ut-docs#1388)

**Date:** 2026-09-01
**Card:** universaltill/ut-docs#1388 — "Till Designer contextual ? opens Catalog help instead of Designer help"
**Complexity:** easy (Dev: Sonnet inline; Review: fresh-context Sonnet subagent, per model-routing rules)

## What shipped

The `?` contextual-help link on `/designer` (Till Designer — arranging
sale-screen quick-sale buttons) opened **Catalog, variants & barcodes**
instead of documenting Till Designer itself. Root cause: `catalog.md`'s
front matter claimed `routes: [/catalog, /import, /designer]` in all four
shipped locales, and no topic existed for `/designer` on its own — a stale
claim from before Till Designer had its own manual page. Oddly,
`catalog.md`'s own prose (step 7) already described using "the screen
designer" to add quick-sale buttons — content that belonged to a
dedicated Till Designer topic, not Catalog.

Changes:

- New topic `web/help/{en,fa,tr,ar}/till-designer.md` (id `till-designer`,
  `routes: [/designer]`, section "Setting up your shop", order 115),
  documenting search-to-add, ▲/▼ reorder, and ✕ remove on the Till
  Designer page, with a one-line disambiguation against the separate
  Receipt & screen designer topic. Titles match the in-product
  `designer.title` locale string exactly in all four locales.
- `catalog.md` in all four locales: removed `/designer` from `routes:`;
  replaced the old step 7 (screen-designer instructions) with a pointer
  to the new Till Designer topic instead of duplicating its content.
- `internal/pages/designer_page_test.go`: new
  `TestDesigner_HelpHintResolvesToOwnTopic`, asserting the rendered
  `data-testid="help-hint"` link on `GET /designer` is `/help/till-designer`.
- `internal/pages/help_hint_test.go`: `TestHelpHrefMapping`'s table gets
  `"/designer": "/help/till-designer"`.
- `web/help/img/{en,fa,tr,ar}/till-designer.png` + `web/help/img/manifest.json`
  regenerated via `make docs-shots` (pre-installed Chromium).

No production Go code changed — this is a docs/help-topic + test-only fix
(no repository/handler/money/plugin surface touched).

## Independent review

A fresh-context Sonnet subagent (per `complexity:easy` routing) reviewed
the diff in an isolated worktree, with no visibility into the implementation
reasoning. Findings: **no blocking issues.**

- Verified front matter (id/routes/section/order/keywords) for all 4 new
  topic files — no route or order collisions.
- Verified no leftover `/designer` mention anywhere in any locale's
  `catalog.md`.
- Cross-checked all 4 titles against `web/locales/*.json`'s `designer.title`
  key — exact match (en "Till Designer", fa "طراح صندوق", tr "Kasa
  Tasarımcısı", ar "مصمّم الصندوق"). Confirmed fa/tr/ar prose are genuine,
  idiomatic translations, not garbled or left in English.
- Ran the full gate independently: `gofmt -l .` (clean), `go vet ./...`
  (clean), `go build ./...` (clean), `go test ./...` (all green),
  `guard-help-topics.sh`, `guard-docs-shots.sh`, `guard-i18n.sh`,
  `guard-compliance-claims.sh` (all pass).
- **Independently re-verified the TDD claim** (not taken on trust): deleted
  the 4 new topic files and reverted `catalog.md`'s `routes:` back to
  including `/designer` in all 4 locales, keeping the new test code —
  confirmed both new assertions fail with exactly the reported bug
  (`hint on /designer → /help/catalog, want /help/till-designer`), then
  restored the fix and confirmed both pass again.
- Confirmed the diff touches zero file-writing/handler code, so the two
  recurring bug classes (missing `os.MkdirAll`, cwd-relative path instead
  of `paths.Data(...)`) don't apply.
- Scope check: `manifest.json`'s diff touches only the `catalog` and
  `till-designer` entries plus the top-level `surface_sha256` — no
  unrelated topic screenshots leaked into the commit (the full `make
  docs-shots` run regenerates all 24 topics' screenshots as a side effect;
  the 4 incidentally-changed, unrelated PNGs — `sell`/`invoices` in en/fa,
  almost certainly a rendered timestamp — were reverted before commit to
  keep the diff minimal).
- No secret-shaped literals or real client/shop names in the diff.

**Deferred, non-blocking, filed separately:** `designer.md` (the Receipt &
screen designer topic) still has a leftover, now-redundant line about
using "the screen designer" for quick-sale buttons. Pre-existing, outside
this card's scope — noted as a Backlog follow-up rather than expanded into
this fix.

## Verified beyond automated tests

- `make docs-shots` actually ran (Playwright against the pre-installed
  Chromium) and produced real screenshots for all 4 new-locale renders of
  `/designer`, not placeholder/stub images.
- `guard-help-topics.sh`'s page-route-coverage check confirms `/designer`
  is claimed by exactly one topic (`till-designer`) with no duplicate
  claim anywhere in the tree.

## Safe-to-merge verdict

**Yes.** No blocking findings from independent review; full gate green;
TDD claim independently re-verified red-then-green by both Dev and
Reviewer, separately.
