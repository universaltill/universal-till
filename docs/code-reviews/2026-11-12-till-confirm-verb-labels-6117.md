# Review: hx-confirm verb labels + Keep order (ut-docs#6117)

Date: 2026-11-12 · Lane: lane:cloud-54 · Built by Sonnet, reviewed independently by Opus (fresh context, separate worktree).

## What shipped
- Every `hx-confirm` opener in the till names its action on the dialog's confirm button with `data-confirm-ok`, instead of the generic "Confirm". That is 33 openers: 29 in templates (including the catalog item delete) and 4 Go emitters (`setup_update_check.go`, `update_api.go`, `settings_page.go` ×2). Each emitter escapes its label.
- `web/public/till-confirm.js` gains a `data-confirm-dismiss` hook (`opts.dismiss`). Both button labels are reset on every ask from the shell's `data-default-label`; `base.html`'s cancel button gained one. A label from one dialog can never leak into the next.
- The open-order and parked-order cancel dialogs read [Keep order][Cancel order]. Focus still goes first to the safe button (Keep order).
- The `app.js` path that re-asks before removing a tile during an unsaved reorder passes the same tone, ok and dismiss.
- 3 new keys (`confirm.remove`, `confirm.delete`, `confirm.keep_order`), each with `en.meta.json` metadata (review standard, max_length). All other sites reuse the key of their own visible button.
- `#ut-confirm` buttons at ≤480px: `flex: 1 1 7.5rem`, the text wraps, and the button is at least 44px tall. Logical properties only; 1024×600 is unchanged.
- Tests: Go guard `TestHxConfirm_EveryOpenerNamesItsVerb` plus a checker self-test, the order-cancel dismiss test and the JS wiring tests; e2e `e2e/tests/till-confirm-labels-6117.spec.ts`.
- Help: step 5 of `web/help/en/open-orders.md`. `CLAUDE.md` states the new rule.

## Findings
| # | Sev | Finding | Outcome |
|---|---|---|---|
| M1 | Medium | The guard matched only `hx-confirm="`, so a Go emitter in an interpreted string (`hx-confirm=\"%s\"`) without a label passed. The "go concatenated literal ok" self-test passed for the same reason. | **Fixed.** The Go branch now unescapes `\"` before it scans, and a new self-test case "go interpreted string missing" was added. It fails without the fix ("want 1 misses, got []") and passes with it. |
| L1 | Low | A single-quoted `hx-confirm='…'`, or a label inside a different `{{ if }}` branch, is not caught. | Accepted: no such shape exists in the tree. The runtime still falls back to the shell's label. |
| L2 | Low | The CSS comment said "9rem basis… stack", but the code uses 7.5rem. | Fixed: the comment now matches the code. |
| L3 | Nit | The `opts.dismiss` doc line in till-confirm.js had the wrong indentation. | Fixed. |
| L4 | Low | The contract was not documented. | Fixed in the universal-till `CLAUDE.md`. The ut-docs pattern doc is deferred to ut-docs#6150, because ut-docs CI was down (ut-docs#6033). |
| Info | — | The JS Go test is a string grep only. The e2e "labels never leak" test is the real leak guard. | Re-verified (below). |

## Verification
- Ran `gofmt -l` (clean), `go build ./...`, `go vet` on the touched packages, `golangci-lint run ./...` (0 issues) and `go test ./internal/pages/... ./internal/httpx/... ./internal/ui/...`. All green.
- Guards: i18n, i18n-meta, rtl-logical-css, no-native-confirm, no-showmodal, help-topics, compliance-claims and data-access are green. `guard-catimport-version`, `guard-deadcode-baseline` and the shellcheck version guard could not run locally (no tags / older Go / no binary); they are left to CI.
- Playwright: the 6117 spec 4/4. The regression set passes 55/55 (till-confirm-5524, jiggle-dirty, escape-focus, native-confirm-6116, open-orders-cancel-3582, parked-orders-popup-2137, pair-confirm, plugin-confirm, sell-tile-hide-delete).
- TDD re-verified by the Tester and the Reviewer, each change reverted afterwards:
  - Removing `data-confirm-dismiss` fails the Go and e2e tests.
  - Removing the cancel-label reset fails the Go and e2e tests.
  - Making the reset conditional (`if (opts.dismiss)`) fails only the e2e no-leak test.
  - Reverting `app.js` fails `TestTillConfirmJS_DismissReset`.
  - Removing the label from the Revoke opener fails the guard, which names the file and line.
- Driven run (Tester, Chromium, every screenshot viewed): I drove the real open-orders page and the real parked popup. I checked them at 1024×600 in light, dark and with the kiosk body class, in fa RTL at 1024×600 and at 360×640, and at 360×640 with a forced long German pair.
  - Keep order and Escape send 0 POSTs. Cancel order sends exactly 1 POST once it is armed.
  - The buttons are 46–51px tall, with no clipping and no horizontal scroll.
  - Spot checks of Revoke, Remove and Change role showed the right labels and tone, and the next plain confirm showed Cancel/Confirm (no leak).
  - Limits: touch and kiosk mode were emulated, not run on real hardware. The fa pack has no `confirm.keep_order` yet, so that label shows in English until Weblate supplies it.

## Verdict
Approved after M1 was fixed.

## Deferred
- ut-docs#6148: the JS `UT.confirm` call sites get verb labels, and all "Update now" confirms use one tone.
- ut-docs#6149: the safe button shows no visible focus cue when the dialog is opened by tap or mouse (this predates the change).
- ut-docs#6150: ut-docs list-and-dialog pattern doc and coding-standards §10.
- Translations of the 3 new keys come through Weblate (ADR-0160, advisory).

Pre-flight: till user-visible behaviour → `web/help/en/open-orders.md` updated, no docs-shots needed (no screenshot shows this dialog), `CLAUDE.md` updated, ut-docs `reference/list-and-dialog-pattern.md` deferred to ut-docs#6150, README not stale; new en.json keys → `en.meta.json` updated (3 keys); plan/price, platform claim, my./admin UI, ADR, plugin type/host function/manifest field → n/a; this review record.
