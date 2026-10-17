# Review — LAN TLS key rotation: manager "rotate now" (ut-docs#4423)

**What shipped.** ADR-0114 §7's on-demand rotation trigger.
- `internal/lantls`: `(*Cert).RotateNow` creates the pending next key at runtime through the same `createNext` the 4-year load step uses (`next-key.pem`, `next-cert.pem`, `rotation.json`), so the rest of the rotation (announce, confirmations, a switch at load once every till confirms or 14 days pass) is unchanged.
  - A pending rotation is kept and its pin returned.
  - An unusable `next-key.pem` is never overwritten.
  - No TLS gives `ErrNoTLS`.
  - `NextPin`/`NeedsConfirm`/`ConfirmNext` now read the pin under `mu`.
- `internal/fleetlink`: `(*Hub).ResendHello` queues a fresh hello (built once, `PeerTillID` per link) on every live link, so joined tills hold `next_pin` without reconnecting.
- `POST /api/tills/rotate-tls-key`: `sync_management`, main till only, localized refusal with TLS off, audited `till`/`lan_tls_rotation_started` when a rotation starts. It is in the demo write list.
- Tills page roster: a "Network security key" line with **Change key** (`hx-confirm`), hidden while a rotation is pending.
- 6 keys in en/ar/fa/tr, help `multitill` step 9 in en/de/ar/fa/tr, regenerated docs-shots manifest, and e2e `tills-network-key-4423.spec.ts`.
- Packs (same cycle, after core merges): ut-plugin-language-de/es/pt.
- Scope split: rotation when a data directory moves to new hardware is ut-docs#4450. In-app backups don't carry the TLS key.

**Independent review (Fable; author Opus 5.5).** Verdict: safe to merge, with no blockers or majors.
- Fixed:
  - (1) A second press now resends the hello too, so a link that missed the first one (full queue, linked later) catches up.
  - (2) `ResendHello` built the hello, with its three DB cursor reads, once per link. It now builds it once.
  - (5) Added a comment noting that a switch within `KeepPrevious` of the last one overwrites the older retired key, which is never served.
  - (8) A comment now cites the right doc.
- Accepted:
  - (3) The joined-till 409 is plain English, as in `till_update_now.go`. The UI never reaches it, because the button only renders on the main till.
  - (4) `RotateNow` holds `mu` across fsync'd writes. It is a one-off press and only briefly delays `NextPin` readers.
  - Nits (6, 7): the side effects a mid-link hello has on the replica are idempotent, and an older replica that ignored it would learn the pin at its next reconnect, inside the window.
- The reviewer re-verified the TDD claims. Removing the pending-keep/unusable-key guard fails `TestRotateNow_PendingRotationIsKept` and `_UnusableNextKeyIsNotReplaced`. Stubbing `ResendHello` fails the hub, client and handler end-to-end tests.
- Translation review (Sonnet): de and pt were clean. es used informal "Tus/Cámbiala" in one key against the pack's usted style; it was fixed.

**Verified beyond unit tests.**
- `go test -race` on lantls/fleetlink.
- `go test ./...`, all green except `internal/plugins`. That package timed out at 10 min on this container with no failing test (it was 2 s into `TestHostTCPCrossPluginHandleRejected`) and doesn't depend on any changed package. CI runs it.
- golangci-lint: 0 issues. Every guard in ci.yml's build job passes, except shellcheck-version (no binary here). guard-deadcode passes on the changed code; its two `internal/logging` hits come from the desktop root, which is skipped here.
- guard-docs-shots is fresh.
- Playwright e2e: 3 passed. The line fits at 1024×600 and 360×800 with no sideways scroll, and the button is at least 44 px. The press shows "in progress" and hides the button.
- Screenshots looked at: en at 1024×600, fa (RTL) at 1024×600, and 360 px. All are clean.
- Not checked: the app's dark theme (it is a till setting, not prefers-color-scheme), and a real two-till run over pinned TLS. That path is covered by the handler test over a real link socket plus the #4409 replica tests.

**Verdict:** safe to merge.
