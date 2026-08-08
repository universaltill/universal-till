# Dead cross-device primaryURL in sync-chip data map (ut-docs#408)

## What shipped

`internal/pages/sync_admin.go`'s `GET /ui/sync-chip` handler passed an
unused `"primaryURL": primary` entry into `web/ui/partials/sync_chip.html`'s
template data map. The template never reads `.primaryURL` — it only
mentions it inside a `{{/* ... */}}` comment (added for ut-docs#405)
explaining why the replica chip deliberately links only to the local
`/tills` route, never the primary's own cross-device origin. That comment
exists because linking a kiosk straight to another device's URL is exactly
the bug class that recurred three times before (ut-docs#147/#148, #159,
#390) — an operator-facing dead end.

Filed by the independent reviewer on ut-docs#390's own review as a latent
risk: nothing stopped a future edit to `sync_chip.html` from starting to
render `.primaryURL` as a bare link, becoming occurrence #4.

Fix, following the card's simpler of its two acceptable options:

- `internal/pages/sync_admin.go`: removed the dead `"primaryURL": primary`
  entry from the map literal. The `primary` variable stays live — it's
  still the guard condition (`if primary := d.SyncPrimaryURL(r.Context());
  primary != ""`).
- `internal/pages/sync_admin_test.go`: added an assertion to the existing
  `TestSyncChip_ReplicaMode` that the rendered chip body never contains the
  literal primary URL, anywhere — not just in `href`. This is a forward
  regression guard for the underlying risk (any future leak of the
  primary's URL into the chip's HTML), not a reproduction of a pre-existing
  bug: the output was already byte-identical before/after this diff, since
  the template never consumed the removed key. TDD in the classic
  red-then-green sense doesn't apply to a dead-code deletion with no
  observable behavior change; the independent review mutation-tested the
  new assertion instead (see below) to confirm it isn't a tautology.

No i18n/money/offline-first/repository-pattern surface touched. No
shop-owner-visible behavior changes — rendered HTML is unchanged.

## Independent review (Sonnet, fresh context — complexity:easy per routing)

Verdict: **safe to merge**, no findings (blocker, minor, or informational).

Independently re-derived the diff from `git show`/`git diff` rather than
trusting the brief, read `sync_admin.go` and `sync_chip.html` in full,
repo-wide grepped for any other consumer of a `"primaryURL"` template-data
key (found only the unrelated `pairing_join.go` struct field and
`sync_api.go`'s separately-keyed `"SyncPrimary"` passed to a different
template — both out of scope, untouched). Ran `go build ./...`,
`go vet ./...`, `gofmt -l`, the targeted `TestSyncChip_*` tests, the full
`internal/pages/...` package suite, and `guard-data-access.sh` — all green,
pasted real output.

Went further than a pass/fail check on the new test: constructed three
regression scenarios (template starts reading `.primaryURL` into `href`
with the map key absent; into a `data-primary` attribute with the map key
absent; into `data-primary` **with** the map key restored) to confirm the
new assertion catches a real leak class the pre-existing `href="/tills"`
assertion does not cover, rather than being redundant with it. All three
were applied and reverted in the reviewer's own isolated worktree, keeping
the actual PR branch untouched throughout.

## Verified beyond automated tests

- `gofmt -l` clean on both changed files.
- Repo-wide grep confirms no other code path reads this specific map's
  `.primaryURL` (as opposed to the differently-scoped `pairing_join.go`
  field of the same name).
- No real client/shop name, no secret-shaped literal in the diff (only
  literal is the test's pre-existing `http://primary.example` fixture
  value).
- No visible surface changed, so no screenshot/manual update applies —
  `web/help/` is unaffected.

## Docs-shots regeneration

Touching `internal/pages/sync_admin.go` puts this diff inside
`guard-docs-shots.sh`'s hashed surface, so CI correctly failed until the
manual's screenshots were regenerated (`make docs-shots`) even though this
particular change has no visible effect. Ran it locally — this session's
pre-installed Chromium (`chromium-1194`) predates the pinned
`@playwright/test@1.61.1`'s expected `chromium_headless_shell-1228`
(exactly the generation-environment mismatch already tracked as
ut-docs#370), so the full non-headless-shell `chromium-1194` binary was
used instead via a **temporary, uncommitted**
`launchOptions.executablePath` override in `playwright.docs.config.ts`,
reverted immediately after the run (`git diff` on that file is empty in
this PR). All 60 screenshots (15 topics × 4 locales) passed and were
committed along with the regenerated `manifest.json`. 10 of those PNGs
differ from before (alerts/designer/users, all locales) — expected
nondeterminism from dynamic on-screen content (a live timestamp in
`alerts`'s "Recent problems" list, present pre-diff too), not a
regression; verified by reading the regenerated `en/alerts.png` directly.

## Deferred / out of scope

None — the card's two listed options were "delete" or "annotate as
gated"; deletion fully satisfies the acceptance criteria on its own.
