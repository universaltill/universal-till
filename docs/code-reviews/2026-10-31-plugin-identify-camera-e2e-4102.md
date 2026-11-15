# Review: plugin identify overlay — camera error e2e (ut-docs#4102)

Date: 2026-10-31 · Lane: lane:cloud-41 · Author: Sonnet subagent (Dev/Tester) · Reviewer: Opus, fresh context

## What shipped (test-only)
- `internal/plugins/testdata/identify_guest` — wasip1 test guest answering `catalog.identify` with a
  canned `SeamSellIdentify` document (optional text + one `add_to_basket` suggestion; `sku`/`label`/`text`
  from its plugin settings).
- `e2e/seed_identify_plugin` — installs it on a spec's own worker till via DB rows (`local-e2e-fixture`),
  the precedent of `e2e/seed_api_importer` (#4431). Not signed: like that precedent, the till loads a
  seeded local fixture at boot. Only `events:receive` is needed — the ADR-0153 data-class gate applies only
  to `ai.agent` plugins.
- `e2e/tests/plugin-identify-camera-errors-4102.spec.ts` (kiosk 1024x600, own till): no
  `navigator.mediaDevices` → `ai.identify.camera_error`; `NotAllowedError` → permission denied;
  `NotFoundError` → not found; `NotReadableError` → busy (each then closes and clears); happy path
  capture → plugin suggestion → tap adds the line, closes the overlay, stops the camera tracks.
  Restores the coverage the #2851 removal of `camera-error-branching-ai-identify-1559.spec.ts` dropped.

## Findings (all low) and outcome
1. Basket assertion matched all of `#basket` (incl. the "Customers also buy" strip) — **fixed**: exact
   `#basket-lines .line-name` match.
2. Seeded `sku` equalled the guest default, so the settings path was untested — **fixed**: a non-default
   `label` is seeded and asserted.
3. Comments named `001_init.sql` as the SKU source; it is `internal/data/seeddata/demo_catalogue.sql` — **fixed**
   in the new files (548/423 specs carry the same stale comment; left, trivial).
4. "Not a modal" comment overclaimed — **fixed**: reworded as a structural check.
5. Minor redundancies (data-* wiring test, status-empty-after-open) — **accepted**, cheap and explicit.
6. First spec importing a JSON file (`en.json`) — **accepted**; Playwright's transform handles it, ran green.

## Verified
- Spec 6/6 green (twice, after fixes 48.3s); gofmt, `go build ./...`, `go vet` (host + wasip1 guest),
  `golangci-lint run ./...` 0 issues; ci.yml build-job guards pass except env-only failures in this
  container (`shellcheck` missing; `deadcode` built with go1.26 vs go1.27 module).
- False-pass re-verified by the orchestrator: `NotReadableError` branch pointed at the generic message →
  that test failed ("Camera unavailable" vs busy text); restored, `git diff web/public/app.js` empty. Dev
  also broke the stop-tracks-on-close step → happy path failed on `__stopCalls`.
- CI pickup: `e2e.yml` runs the `default` project; the filename matches no `testIgnore` set.

## Verdict
Safe to merge. No product code, migration, locale key or help topic changed.
