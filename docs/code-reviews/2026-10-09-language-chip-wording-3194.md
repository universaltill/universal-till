# Review — pending language-pack chip no longer promises a pack that may not exist (ut-docs#3194)

Date: 2026-10-09 · Branch: `fix/3194-language-chip-wording` · Lane: `lane:cloud-41`
Author model: Opus 5.5 · Reviewer: Fable (independent subagent, fresh context)

## What shipped

- An offline till queues a pending `language/<country-locale>` base-plugin
  spec whether or not a pack is published for that locale (FR/IT/NL/PK have
  none). The Settings → Data chip said "Installing your free FR language
  pack in the background…", then the spec was silently dropped once the
  catalog answered with no listing. The AC allowed either queue-time
  filtering or softer wording; offline the till cannot know, so the copy
  changes instead.
- `setup.base_plugins.pending` (existing key, so the language packs have no
  key drift) now reads "Looking for a free %s language pack — if the plugin
  catalog has one, the till installs it in the background." in en, with
  matching ar/fa/tr values.
- New test `TestSettingsPendingLanguageChipDoesNotPromiseUnpublishedPack`:
  dead marketplace → spec stays pending and the chip says "Looking for…",
  never "Installing…"; reachable catalog with no FR listing → spec dropped
  and the chip disappears.
- Template comment cites #3194; the docs-shots `surface_sha256` was
  refreshed for that comment-only edit (`Docs-Shots-Unchanged: true`). No
  docs-shots screenshot shows this chip.

## Findings (Fable review)

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | Medium (cross-repo) | `ut-plugin-language-{de,es,pt}` still carry the old "installing…" value; the drift check compares keys, not values. | Follow-up Backlog card ut-docs#4011 (value-only PRs in the three packs). |
| 2 | Low | The first English draft ("…as soon as the plugin catalog has one") implied the catalog lacks the pack even for an offline DE/ES/PT till; the translations already used a conditional. | Fixed: English is now conditional, matching ar/fa/tr. |
| 2b | Info | A published pack whose install keeps failing still shows "Looking for…" (the old copy was equally wrong). | Accepted, out of scope; no "install failing" chip state exists for language specs. |
| 3 | Nit | Template comment could cite #3194. | Fixed. |
| 4 | Nit | Test called `resetTaxCatalogForTest` needlessly. | Fixed. |

## Verified beyond automated tests

- TDD: the test was written first and failed on the old copy ("chip must
  not promise…", "chip must say the till is looking…"); passes after.
- Driven run: a throwaway till built from this branch (fresh data dir,
  marketplace unreachable) with a pending `language/fr` spec. Screenshots
  of Settings → Data at 1024×600 (en, fa) and 360×800 (en, tr) were
  inspected: the chip wraps cleanly inside the card, RTL is correct for fa,
  and the Remove button is untouched.
- Gate: `gofmt`, `go build ./...`, `go test ./...`, golangci-lint, the
  `ci.yml` build-job guards. Environmental-only failures locally:
  `guard-commit-attribution` (needs a commit range on stdin),
  `guard-deadcode-baseline` (local deadcode tool built with an older Go) and
  `guard-gobind-skip` (no gobind). CI runs all three.

## Verdict

Safe to merge. Deferred: language-pack value catch-up (ut-docs#4011).
