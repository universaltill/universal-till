# Review: msstore-verify helpers lacked the exec bit (2026-11-26)

Second promote-release dry-run on v0.36.4 (run 45872461276): `msstore-verify.sh` line 123 "Permission denied" running `scripts/ci/check-installer-silent.sh` directly. `msstore-soak.sh` had the same mode 100644. Fix: mode 100755 on both; msstore-workflow_test.sh asserts all three default helpers are executable. Mode-only change plus one test; no permission, secret or trigger change. Verdict: approved.

Pre-flight: scripts/tests only; no Go, i18n, UI or ADR change; shellcheck and guard-pipefail-grep-q pass.
