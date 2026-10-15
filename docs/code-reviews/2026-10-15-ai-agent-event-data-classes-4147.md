# Review — events need ai.send.<class> for an ai.agent plugin (ut-docs#4147)

## What shipped
- **Event data-class registry** (`internal/plugins/event_dataclass.go`):
  - `sale.completed` needs sales, customers and staff.
  - `stock.adjusted` needs stock; `customer.erased` needs customers.
  - `catalog.identify` and `catalog.identify.confirmed` need catalog.
  - `ui.view.ask`, `ui.action.ask`, `plugin.button.pressed` and `agent.action.result` need no grant.
  - The plugin's own events need no grant either: its schedule ticks (`Event.Scheduled`), and events it published under its own prefix (`Hop > 0`, `Event.Publisher == id`).
  - Any other event is refused to an `ai.agent` plugin (fail closed).
  - The design is the ADR-0153 amendment of 2026-10-15.
- **One check point**, `WasmRuntime.handleEvent`:
  - It runs before a call slot is taken or the guest is instantiated. Every path reaches it: bus Publish/Ask/AskPlugin, the drainer, UI asks, jobs, schedule ticks.
  - Grants are checked live with the non-auditing repo check.
  - Whether a plugin declares `ai.agent` is cached per plugin at `Sync`, before `load`. A plugin without it pays no extra read per event, which matters on the sale path.
  - An unreadable manifest marks the plugin broken and refuses every event (the #2280/#2304 precedent). A missing manifest falls back to the declared `ai.agent` permission row.
- **A refusal is `ErrEventDataClassDenied`**:
  - A blocking publish (payment authorize/refund) returns an error. It never returns (nil, nil), which would read as approval.
  - Ask/AskFrom/AskPlugin skip the plugin like a refused `events:receive`. They write an `event_dispatch` `denied` row.
  - Drainers log it at Debug.
- **Audit**: a `permission_denied` row naming the event and the missing `ai.send.<class>`, or `ai.agent` for an unclassified event. It is coalesced per plugin and event type per minute, with a count (ut-docs#3887 shape).
- **Docs**: the `ai.agent` row in `docs/plugin_guidelines.md`. The ut-docs ADR-0153 amendment and `architecture/wasm-runtime.md` are in ut-docs.

## Review
Independent review on Fable. Dev was Opus 5.5.

| # | Severity | Finding | Outcome |
|---|---|---|---|
| 1 | MAJOR | "Own event" was any hop-0 event under `<id>.`. Core publishes manifest-named payment and button `trigger_event`s (sale id, amount, reference) and the derived `.authorize`/`.refund` hooks at hop 0 under whatever name the manifest chose, so an `ai.agent` payment plugin got every tender ungated. | **Fixed**: own = `Scheduled \|\| (Hop > 0 && Publisher == id)`. The test now refuses `<id>.pay.authorize` at hop 0. |
| 2 | MINOR | A missing `manifest.json` (`InstalledManifest` ok=false) left the gate off, while the view path refuses in that state. | **Fixed**: falls back to the declared `ai.agent` permission row. New test `TestEventDataClass_MissingManifestFallsBackToDeclaredRow`. |
| 3 | MINOR | The docs' own-event wording disagreed with the code. | **Fixed** in the ADR amendment, wasm-runtime.md and plugin_guidelines.md. |
| 4 | MINOR | UI asks carry the operator's user id and role with an empty class set. | **Accepted, documented**: ADR-0153 Decision 3 needs it, and it is the operator's own request. The amendment now says so explicitly. |
| 5 | NIT | The `AskPlugin` comment said "another subscriber may answer". | **Fixed**. |
| 6 | NIT | Throttle keys are not dropped on uninstall. | **Accepted**: bounded by plugins × event types. |
| 7 | NIT | The first acceptance test fails on the audit wait before the guest-ran assertion. | **Accepted**: it still fails without the gate. |

The reviewer confirmed:
- Every guest-running path goes through `handleEvent`. Process plugins never subscribe to the bus.
- The gate is written before `load` under `w.mu`.
- Payment and refund callers treat any error as a decline.
- `Event.Publisher` is `json:"-"` and set only from the host's own plugin id, so a guest cannot forge it.

## Verification
- **TDD re-verified by the reviewer** in a separate worktree. With the gate stubbed out, `TestEventDataClass_AIAgentSaleCompletedNeedsAISendSales` and `TestEventDataClass_BlockingRefusalIsAnError` fail; restored, they pass.
- `go build ./...`, `go vet`, `gofmt` and `golangci-lint` are clean. 18 build-job guards pass. The deadcode baseline adds nothing new.
- `go test -race` on the new tests passes, and so do the internal/pages and internal/data suites.
- `go test -timeout 20m ./internal/plugins` (the CI config) passed before the review fixes. The new tests and the affected subsets were re-run after them.
- No UI surface changed, so no screenshots and no help topic.

## Verdict
Safe to merge after fixes 1–3.

## Deferred
- None as new cards. Validating `trigger_event` against the plugin's namespace would be hardening, but the gate no longer depends on it.
