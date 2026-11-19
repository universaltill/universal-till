# Review — Tills page shows the main till's discovery "Till ID" (ut-docs#1294)

**Date:** 2026-11-08 · **Branch:** `fix/1294-show-discovery-till-id`
**Author model:** Sonnet (dev subagent) · **Tester:** separate subagent · **Reviewer:** Opus 5.5 (independent subagent, separate worktree)

Pre-flight: web/help updated (`multitill.md` steps 3 and 10; docs-shots `manifest.json` surface + multitill.en hashes, no PNG changed) · reference/ n/a (no reference page names either id) · README n/a · en.json n/a (no new key; reuses `tills.discovery.result_name`) · i18n-meta updated (metadata added, baseline line removed) · ADR updated (ADR-0033 amendment + `adr/README.md` index, ut-docs `docs/1294-discovery-till-id`) · review record: this file + ut-docs `code-reviews/2026-11-08-two-till-ids-1294.md` · follow-ups: ut-docs#5893 (promote keeps the old main till's discovery id).

## What shipped
The LAN scan labels a main till's discovery id (`lan_discovery.till_id`, the mDNS TXT `id=`) "Till ID", but no other till screen showed it, so an operator could not confirm a scanned till was the same device without SSH/adb. Design choice (card option 2, Architect): keep the two ids separate (merging would change ADR-0033 §4 / ADR-0114 §7 proof inputs and need a superseding ADR) and show the main till's discovery id where the operator needs it.
- `internal/pages/tills_roster.go`: `mainTillDiscoveryID` — on a main/standalone till, its own id via the existing `discoveryTillID` get-or-create seam (the same value the advertiser broadcasts; after the first render it is a plain read); on a joined till, `sync.primary_till_id` only — never minted, never the shop-wide `lan_discovery.till_id` copy. A read error logs and renders blank, never a 500. Passed to the roster as `MainTillDiscoveryID`.
- `web/ui/partials/tills_roster.html`: main-till row's name cell gets `Till ID: <bdi dir="ltr">uuid</bdi>` (or `—`), muted small, testid `tills-main-discovery-id`. `web/public/app.css`: `.tills-roster .till-id { overflow-wrap: anywhere; }` so the UUID wraps at 360px.
- `web/ui/pages/tills.html`, `setup.html`: scan result wraps the (still escaped) UUID in `<bdi dir="ltr">` so its hyphen groups read the same as the roster in RTL locales.
- Tests: `internal/pages/tills_discovery_id_test.go` (helper: main mints + stable, joined uses primary id, joined blank neither mints nor falls back, error → blank; template render; real `/ui/tills/roster` handler on a migrated DB) and `e2e/tests/tills-discovery-id-1294.spec.ts` (1024×600 and 360×740, survives a roster poll).

## Review findings
- **R1 (minor, help accuracy) — fixed.** Help said the id shows "—" until the till "has reconnected to its main till once"; `sync.primary_till_id` is stored on the first successful sync contact (`primary_watch.go`). Reworded to "until this till has synced with its main till, usually within seconds of joining".
- **R2 (minor, coverage) — accepted.** The joined-till settings-read error branch has no own test; it shares the main branch's error block, which is tested.
- **R3 (nit) — accepted.** The error log repeats on every 10 s poll while settings reads fail; a failing settings DB is loud elsewhere too.
- **R4 (nit) — accepted.** The e2e waits for the next roster poll (~10 s per viewport); that is the point (value survives a poll).
- Clean: role detection incl. promoted till (shows the id it now advertises — scan and row agree; the underlying promote bug is ut-docs#5893), no lock contention on the polled GET, escaping (scan JS still escapes; template auto-escapes), empty state, RTL/logical CSS, i18n meta, manifest diff limited to two hashes, no raw SQL, no file writes/paths.

## Verification
- Dev TDD: test file alone → `undefined: mainTillDiscoveryID`; implemented → green.
- Tester false-pass check: 7 deliberate breaks; 6 caught by Dev's tests, the 7th (renaming the data-map key) passed them → Tester added the real-handler test, which catches it. Reviewer re-ran 5 breaks in a throwaway worktree; every one failed the new tests; restored → all pass.
- Tester gate: gofmt clean, golangci-lint 0 issues, shellcheck clean, all 143 ci.yml build-job guard/self-test invocations pass (after the docs-shots manifest update), `go test -count=1 ./...` 108 packages ok (`internal/plugins` needs CI's 20m timeout), `-race` on discovery + roster tests clean, Playwright 12/12 (new spec + discovery-1295, tills-lan-discovery, tills-network-key-4423, tills-pairing-layout-1548).
- Driven run (real binaries, Playwright emulation, no real hardware): main till 1024×600 and 360×740 (no horizontal scroll, touch drag scrolls), joined till shows the main's id and never its own, joined till without `sync.primary_till_id` shows "—", scan list same label + id (scan API stubbed with the main's real id — mDNS returns nothing in the container), Persian RTL at both sizes keeps hyphen-group order, value unchanged after a poll, light/dark muted contrast, kiosk mode fine. At 360px the line inherits the phone card's bold heading — cosmetic, accepted.
- After R1: `run-docs-shots-guard.sh` and `guard-help-topics.sh` pass.

## Verdict
Approve (one minor help-wording finding fixed in review).
