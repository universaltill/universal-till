# Review — camera identify job carries a read-only operator (ut-docs#4444)

**Shipped.** `servePluginIdentifyStart` (`internal/pages/plugin_identify.go`)
now starts the `catalog.identify` job with the signed-in operator
(`withAgentOperator`), a live session check (`withAgentJobSession`) and a new
read-only marker (`plugins.WithAskReadOnly`). The runtime copies the marker
into `hostState.operatorReadOnly` (only when an operator was stamped), and
`actionPropose` refuses a read-only operator with `-2`, audited. So, under
ADR-0153 "Who is asking" (amendment 2026-10-15), an `ai.agent` plugin's
identify job reads `catalog.*` views for the cashier who took the photo. Each
view is still gated on `ai.send.<class>` and on the operator's role (a
cashier gets no `sales` view). Reads end when the session signs out, and the
job can propose no change. The ut-docs ADR amendment and the reference
updates land in the companion ut-docs PR.

**Tests (TDD).**
- `TestPluginIdentify_JobCarriesReadOnlyOperator_4444`: the handler, run
  through the real mux with a real auth session. Fails on `origin/main`'s
  `plugin_identify.go` ("operator = {} (stamped false)").
- `TestPluginIdentify_NoSessionNoOperator_4444`: no session means no
  operator.
- `TestActionPropose_ReadOnlyOperatorRefused_4444`: fails without the
  `operatorReadOnly` check (a proposal is created).
- `TestAskOperatorFor_ReadOnlyJob_4444`.
- `TestViewQuery_AIAgentIdentifyJobReadsForCashier_4444`: the real WASM guest
  on a `catalog.identify` job. The catalog read is allowed, `items.top.v1`
  is refused for the cashier (audited `reports`), and every read is refused
  after sign-out.

**Review.** Independent subagent review on Sonnet. A Fable review was
refused: the account is out of usage credits (HTTP 429). A first Sonnet
attempt was confined to the ut-docs worktree and could not review the Go
code, so it was re-run on pre-made worktrees.
- Verdict: safe to merge, no blocker or major finding.
- The reviewer re-ran both TDD reverts (fail without the fix, pass with it).
- The reviewer checked these and found them fine:
  - The stamp comes only from `auth` context.
  - The read-only flag is set only on the identify route.
  - The retry `startPluginJob` after `cancelRunning` shares one request id
    for one capture.
  - With `UT_AUTH=off` there is no operator.
  - `WithoutCancel` keeps the stamps for the job's lifetime.

Findings:
- nit, fixed: the audit reason now says "read-only operator (camera identify
  job)".
- minor, accepted: `actionPropose`'s doc-comment check list is long. The new
  clause is in the right order.
- nit, tracked: `ai.tool.invoke.*` must refuse a read-only operator when it
  is built. The ADR words this as a requirement, and it is noted on
  ut-docs#4428.

**Verified beyond unit tests.**
- Full gate: `gofmt -l .` is empty, `go build ./...` passes, and
  `go test ./...` passes for every package. `internal/plugins` was run
  separately with `-timeout 20m`, as CI does (ok, 721s); under the default
  10m it times out on this shared container.
- `golangci-lint run ./...`: 0 issues.
- The CI build-job guards pass, except two that cannot run here:
  - `guard-deadcode-baseline.sh`: the local `deadcode` binary was built with
    go1.26 against a go1.27 module.
  - `guard-shellcheck-version.sh`: no shellcheck binary.
  CI runs both.
- Not checked visually: there is no UI change, and identify renders exactly
  as before. No installed plugin declares `ai.agent` yet (#4142), so till
  behaviour is unchanged today.

**Safe to merge:** yes.
