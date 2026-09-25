# Review — replica re-discovers its main till (ut-docs#2722)

- **Change:** stable listen port on Android (`internal/listenport`), `discovery.PrimaryWatch` (mDNS re-discovery after 3 failed contacts, at most once per 5 min), `POST /api/sync/primary-proof` HMAC handshake keyed by the bearer hash, main-till status chip on the replica rail, Android `MulticastLock`, help text in en/de/ar/fa/tr, 6 new locale keys.
- **Author:** Claude Opus 5.5. **Reviewer:** Claude Fable 5.1 (independent, different model).

## Findings and dispositions

1. **Medium — the proof could be relayed.** A LAN device advertising the main till's public id could forward the replica's challenge to the real main till and return its valid proof; the replica would then switch `sync.primary_url` to the relay and send its bearer there on the next pull. **Fixed:** the proof now also binds the host:port the replica dialled. The main till signs only when `r.Host` matches the socket that accepted the connection (same IP and port; a hostname only if it resolves to that IP), else 404. A relay can't get a proof valid for its own address. Tests: `TestPrimaryWatch_RelayedProofDoesNotSwitch` (failed first: the replica switched to the relay), `TestPrimaryProofAPI_OnlyAnswersForItsOwnAddress` (5 refusal cases + 1 accepted hostname; failed first: 200 for all), and host/port cases in `TestPrimaryProof_BindsEveryInput`. Tradeoff: a main till behind NAT or port-forwarding can't be found again automatically; pairing it again still works.
2. **Low — desktop/Pi port isn't persisted, but the help text promised a fixed port.** **Resolved by narrowing the help text** (5 languages): persisting would override `UT_LISTEN_ADDR` or the deliberately loopback-only fallback ports (ut-docs#1169). The help now says the port is normally the same and the till's network address may change, which step 2's search handles.
3. **Low — possible WARN noise** on the legacy-hint path: at most once per 5 min per candidate. Accepted.
4. **Info — the chip is polled on unauthenticated screens:** it behaves exactly like the existing sync chip. No change.

Checked clean: `hmac.Equal`; fresh 32-byte replica nonce (no replay); bearer never sent to a candidate; rate limiter bounded (256 sources); `tills.bearer_hash` redacted from the admin bundle; `sync.primary_till_id` per-till; Android lock lifecycle; i18n parity; repository pattern.

## Verification

`go test -race -count=1`: `internal/discovery`, `internal/listenport`, `internal/server`, `internal/auth`, `mobile`, and `internal/pages -run 'Sync|PrimaryProof|MainTill|TillsPage|ReplicaWithStaleURL'` (incl. the end-to-end stale-URL recovery through the real pull loop) — all pass. gofmt, go vet, golangci-lint, guard-i18n, guard-help-drift, guard-help-topics, guard-data-access: clean. Android build and real-device checks (tablet + Windows VM + Pi5-1) remain: android-ci on the PR, device test after release.
