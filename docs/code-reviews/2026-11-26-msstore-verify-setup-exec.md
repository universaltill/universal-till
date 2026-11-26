# Review: msstore-submit verify ran a non-executable script (2026-11-26)

Found by the first real run of promote-release (dry run, v0.36.4, run 45868152727): `microsoft / verify` exit 126 because `packaging/windows/setup-verify.sh` is mode 100644 and the step ran it directly. msstore-submit.yml had never run before.

- Fix: `run: bash packaging/windows/setup-verify.sh` (one line). Test: msstore-workflow_test.sh fails if any `run:` executes a packaging script directly (verified red on the old line, green on the new).
- Security: no permission, secret or trigger change. Verdict: approved (one-line fix, covered by the combined review in `2026-11-26-promote-release-store-pipeline.md`).

Pre-flight: workflow + test only; no Go, i18n, UI or ADR change; shellcheck and guard-pipefail-grep-q pass.
