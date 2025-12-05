# Performance & Resilience Quickstart (008)

Target hardware: Raspberry Pi 4 (8GB) or equivalent mini PC. Adjust thresholds via env vars if your runner differs, but keep defaults for baseline checks.

## Thresholds (defaults)
- Sale completion: warn `4000ms`, fail `5000ms` (`UT_BENCHMARK_SALE_WARN_MS`, `UT_BENCHMARK_SALE_FAIL_MS`, legacy `UT_BENCHMARK_THRESHOLD_MS`).
- Micro interactions (lookup/cart add): warn `150ms`, fail `200ms` (`UT_BENCHMARK_INTERACT_WARN_MS`, `UT_BENCHMARK_INTERACT_FAIL_MS`).

## Sale benchmark (CI-backed)
```bash
# Runs in normal test suite; fails if average > fail threshold, logs warning if > warn
go test ./internal/pos -run TestSalePerformanceThresholds

# Optional benchmark mode for deeper sampling
go test -bench=BenchmarkCompleteSale -benchtime=10x ./internal/pos
# Override threshold
UT_BENCHMARK_SALE_FAIL_MS=4000 go test -bench=BenchmarkCompleteSale ./internal/pos
```

## Micro-interaction benchmark
```bash
go test ./internal/pos -run TestMicroInteractionLatency
# Override micro thresholds if needed
UT_BENCHMARK_INTERACT_FAIL_MS=250 go test ./internal/pos -run TestMicroInteractionLatency
```

## Offline smoke (sale flow)
```bash
go run ./scripts/smoke-offline-sale/main.go               # uses ./data/smoke-offline-sale.db
go run ./scripts/smoke-offline-sale/main.go /tmp/smoke.db # custom path
```
- Fails on setup/flow errors; exits with warning code if duration exceeds 5000ms.

## Event dispatch rules (summary)
- Default: non-blocking plugin events with audit of outcomes; failures should not block core flow.
- Blocking events must be explicitly marked, wrapped in a transaction, and roll back on handler failure while emitting audit entries.

## CI behavior & semantics
- **Warnings**: Tests log warning to stdout but **exit 0** (pass). Warnings indicate potential regression; investigate before merge.
- **Failures**: Tests **exit 1** and **block PR merge**. Must fix code or justify threshold increase with hardware evidence.
- **Overrides**: Set env vars in GH Actions workflow to adjust for runner differences:
  ```yaml
  env:
    UT_BENCHMARK_SALE_FAIL_MS: 6000  # Slower CI runner
    UT_BENCHMARK_INTERACT_FAIL_MS: 250
  ```
- **Local dev**: Override thresholds without affecting CI defaults.

## CI expectations
- `go test ./...` runs both performance checks with defaults on GH runners.
- Treat warnings as regressions to investigate; failures must be fixed or thresholds justified for slower hardware with explicit env overrides.
