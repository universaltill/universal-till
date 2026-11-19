# Dead-code baseline: three buildinfo channel entries (ut-docs#6388)

Pre-flight: baseline-only change; no user-visible, i18n, ADR or plan surface (n/a).

`RequireExplicitChannel` and `SetChannelOnce` are called only from `mobile/mobile.go`, which the deadcode roots do not reach (same case as `SetAndroidBridge`). `OverrideChannelForTest` is a cross-package test seam used by pages and updates tests (deadcode runs without tests), same precedent as `ResetCacheForTests`. Reviewed in the batch review: justified. Verdict: approve.
