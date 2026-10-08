# Review: pending tax plugin says "not in the catalog yet" (ut-docs#3243)

Branch `fix/3243-pending-tax-not-published`. Author: Opus 5.5 (lane:cloud-24).
Independent review: Fable, fresh context, in a detached worktree.

## What shipped

- Since ut-docs#3210 a consented `{tax, de}` spec stays on the #591 pending
  list when the catalog is reachable but has no listing. The Settings chip
  still said "Installing your DE tax plugin in the background — this can take
  a few minutes if you're offline", which is untrue for an online till.
- Each install attempt (`resolveAndInstallBasePlugin`, deferred
  `recordBasePluginAttempt`) records why a tax spec is still pending in a new
  per-till setting, `setup.pending_base_plugins_not_published`. It lives under
  the existing per-till prefix `setup.pending_base_plugins`. It is a separate
  key, not a field on `basePluginSpec`, because that struct is compared by
  value as a map key and with `==` in many places. The setting is written
  only when its value changes.
  - "not published" → set, but only while the spec is still pending;
  - success or an unreachable catalog → cleared;
  - cancelled attempts, DB errors and install errors → left unchanged.
- Settings chip: new key `setup.base_plugins.not_published_tax`. The
  "offline" copy stays for an unreachable catalog.
- Setup step 3's queued branch (ut-docs#3244, which renders only when there is
  no catalog match): new key `setup.tax_plugin.not_published` replaces "still
  installing".
- Step 3 offline re-render after consent, with no `tax_plugin_pending` query
  param: shows the queued note instead of a fresh "Install when online" offer.
  The card's comment asked for this.
- The pending chip used the monospace, `nowrap` code `.chip`, so the sentence
  ran off the card at 1024px and at 360px. The new `.chip-note` variant uses
  the body font and wraps, in the same way as `.chip-warn` (ut-docs#2648).
  The language-pack chip uses it too: a visible change outside this ticket,
  and a deliberate one, because the same overflow affected it.
- New keys are added to en/ar/fa/tr. The users.md help paragraph is updated in
  en/de/tr/fa/ar; tr/fa/ar never had the #3244 sentence, and now do.
  docs-shots were regenerated (manifest only; PNGs are pixel-identical).

## Review findings

| # | Sev | Finding | Outcome |
|---|---|---|---|
| 1 | Medium | `web/help/img/manifest.json` surface hash was stale after the last edits, so main would go red | Fixed: re-ran `make docs-shots` after the final edit |
| 2 | Medium | Race: if a dismiss happened during an in-flight attempt, the attempt could write the mark back. A later offline re-queue would then inherit it and say "not in the catalog" before any attempt | Fixed: a mark is set only while the spec is still pending (re-read under a mutex). The mark is cleared on enqueue (`addPendingBasePlugins` and the install handler's direct save), and dismiss clears it after its pending-list write. Regression test added |
| 3 | Low | Step 3's queued branch can trust an hours-old cached catalog miss (`setupTaxCatalogEntries` returns a stale cache on a failed fetch). It would then say "not in the catalog yet" while actually offline | Accepted: this is pre-existing cache behaviour (#3244 had the same ambiguity with "still installing"). Follow-up card filed |
| 4 | Low | Any non-catalog error (shutdown cancel, DB error) cleared a correct mark | Fixed: only catalog outcomes rewrite the mark. `errBasePluginCatalogUnreachable` is a sentinel; the log text is unchanged |
| 5 | Nit | Read-modify-write of the mark list was unsynchronised | Fixed: `basePluginsNotPublishedMu` |
| 6 | Nit | `/settings` read the mark list even with nothing pending | Fixed: the read is gated on `len(pendingBasePlugins) > 0` |
| 7 | Nit | `.chip-note` also changes how the language-pack chip renders | Accepted and recorded above |

## Verification

- TDD: four tests are new in the first round and one in the second.
  - The reviewer reverted the production files and confirmed that each of the
    first four failed for the stated reason, then passed once restored.
  - The round-2 test was mutation-checked against three single-line
    regressions: the pending check, the clear on enqueue, and the
    cancel branch. Each mutation made it fail.
- `go build ./...`, `go vet`, `gofmt`, `go test ./...` and every guard in
  `ci.yml`'s `build` job pass locally.
  - `shellcheck` is not installed in this container, so
    `guard-shellcheck-version` could not run; no shell script changed.
  - `golangci-lint` is not run locally: the local binary is built with go1.25,
    below the module's go1.27. CI runs it. Only `unused` is enabled, and every
    new function has a caller.
- e2e: `login.spec.ts` and `settings-pos-notice-918.spec.ts`, 19/19 pass.
- Driven run (the real binary, PIN login, Playwright):
  - A fake reachable catalog with no listings, and the real 30s retry tick:
    the setting was recorded, and the chip reads "Your DE tax plugin isn't in
    the plugin catalog yet…" in en (1024×600), fa (360×800, RTL) and tr.
  - Restarted against a dead endpoint: the tick logged "catalog unreachable"
    and the chip went back to the offline copy.
  - Screenshots were looked at. Before `.chip-note` the sentence was clipped
    to one line; after, it wraps inside the card in both the LTR and RTL
    layouts.
  - Not checked: dark theme, or on real touch hardware (no interaction
    changed).
- Language packs de/es/pt carry the two keys (`check-key-drift.sh` against
  this branch's en.json: 0 drift, 0 orphans). Their PRs land after core
  merges.

## Verdict

Safe to merge once CI is green.
