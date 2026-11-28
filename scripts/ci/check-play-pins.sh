#!/usr/bin/env bash
# Pin pre-check for play-publish.yml (ut-docs#6410, ADR-0194): the upload-cert
# pin must exist and differ from every release cert, and the release-cert list
# must be present, or the "never the release cert" check is vacuous.
# Shared by the build job (--warn-only on a dry run: an empty pin only warns)
# and the sign job (hard fail before any key is touched). A pin equal to a
# release cert, or a missing release list, always fails. Reads no secret.
# Exit: 0 ok, 1 pin problem, 2 usage.
set -euo pipefail
ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
PIN_DIR="${UT_PIN_DIR:-$ROOT/android/app}"
warn_only=0
case "${1:-}" in
  "") ;;
  --warn-only) warn_only=1 ;;
  *) echo "usage: check-play-pins.sh [--warn-only]" >&2; exit 2 ;;
esac
pin_file="$PIN_DIR/play-upload-cert-sha256.txt"
rel_file="$PIN_DIR/apk-signer-sha256.txt"
if [ ! -s "$rel_file" ]; then
  echo "::error::android/app/apk-signer-sha256.txt is missing or empty; it is committed in the repo, restore it (git checkout main -- android/app/apk-signer-sha256.txt). Without it the upload key cannot be proven different from the release key."
  exit 1
fi
if ! grep -qE '^[0-9a-f]{64}$' "$rel_file"; then
  echo "::error::android/app/apk-signer-sha256.txt holds no release-cert digest (64 hex characters on a line, comments do not count); restore it (git checkout main -- android/app/apk-signer-sha256.txt). With no release digest, no release cert is listed and the upload key cannot be proven different from the release key."
  exit 1
fi
pin=""
[ -f "$pin_file" ] && pin="$(grep -m1 -E '^[0-9a-f]{64}$' "$pin_file" || true)"
if [ -z "$pin" ]; then
  msg="android/app/play-upload-cert-sha256.txt holds no upload-cert pin. Owner: run 'bash scripts/ci/set-play-upload-pin.sh <upload.keystore>' locally (runbook step E), open a PR with the changed file, merge, then re-dispatch."
  if [ "$warn_only" = 1 ]; then
    echo "::warning::$msg (dry run: continuing; a real run stops here)"
    exit 0
  fi
  echo "::error::$msg"
  exit 1
fi
if grep -qxF "$pin" "$rel_file"; then
  echo "::error::the upload-cert pin equals a release signer in apk-signer-sha256.txt. Owner: create a SEPARATE upload keystore (runbook step E) and run 'bash scripts/ci/set-play-upload-pin.sh --replace <upload.keystore>'."
  exit 1
fi
echo "play upload-cert pin present and distinct from the release certs"
