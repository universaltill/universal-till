#!/usr/bin/env bash
#
# Wiring + behaviour test for ADR-0181 / ut-docs#5437: every till release
# asset carries a build-provenance attestation, made by two small jobs that
# hold the Sigstore OIDC token and never the Ed25519 release-signing seed.
#
# Wiring pins, in .github/workflows/release.yml:
#   - `provenance` needs checksums; publish-release (and the orphan-tag
#     cleanup) need provenance and gate on its result, so an unattested
#     release stays a draft;
#   - `provenance-dmg` needs macos-dmg-attach (post-publish);
#   - both jobs have id-token: write + attestations: write, no `environment:`,
#     a timeout, only full-SHA-pinned `uses:`, an actions/attest-build-provenance
#     step, and call scripts/ci/verify-release-provenance.sh;
#   - `attestations: write` is in no other job; `id-token: write` only in
#     goreleaser, windows-installer, provenance and provenance-dmg; the
#     workflow-level permissions have neither.
# The verify script must carry (in executable lines, not comments) the --repo/
# --signer-workflow/--source-digest/--deny-self-hosted-runners flags and the negative drill.
#
# Behaviour: verify-release-provenance.sh against a stubbed `gh`.
# Self-test: the wiring checks must FAIL on planted bad workflow copies.
# Readers consume their whole input (here-strings, no `grep -q` on a pipe) so
# pipefail never turns a SIGPIPE into a false "missing" (ut-docs#2941).
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
cd "${ROOT_DIR}"
# shellcheck source=scripts/ci/guard-test-sandbox.sh
source scripts/ci/guard-test-sandbox.sh

WF=".github/workflows/release.yml"
VERIFY="scripts/ci/verify-release-provenance.sh"
QUIET=0
fails=0
pass() { if [ "$QUIET" -eq 0 ]; then echo "ok   - $1"; fi; }
fail() { if [ "$QUIET" -eq 0 ]; then echo "FAIL - $1" >&2; fi; fails=$((fails + 1)); }

JOB_KEY='^  [a-zA-Z_][a-zA-Z0-9_-]*:[[:space:]]*(#.*)?$'
jobs_of() {
  awk '/^jobs:/{on=1; next} on && /^[^ #]/{on=0} on && $0 ~ KEY {sub(/^  /,""); sub(/:.*/,""); print}' KEY="$JOB_KEY" "$WF"
}
job_block() { # job_block <job-name>
  awk -v j="$1" '$0 ~ KEY {k=$0; sub(/^  /,"",k); sub(/:.*/,"",k); if (on) exit; if (k==j) {on=1; print; next}} on{print}' KEY="$JOB_KEY" "$WF"
}
needs_of() { # needs_of <job-name>: job names in its needs:, one per line
  job_block "$1" | awk 'done{next} /^    needs:/{f=1} f{print} f && (/\]/ || /needs: [a-z]/){f=0; done=1}' \
    | sed 's/needs://' | tr -d '[],' | tr -s ' \n' '\n' | sed '/^$/d'
}
needs_has() { # needs_has <job-name> <needed-job>
  local needs
  needs="$(needs_of "$1")"
  grep -qx -- "$2" <<<"$needs"
}
# Lines of a job block that are not comments.
code_of() { grep -vE '^[[:space:]]*#' <<<"$1" || true; }

check_wiring() { # reads $WF
  local j block code
  for j in provenance provenance-dmg publish-release macos-dmg-attach; do
    if [ -z "$(job_block "$j")" ]; then
      fail "release.yml has no ${j} job"
      return 0
    fi
  done

  if needs_has provenance checksums; then pass "provenance needs checksums"; else fail "provenance does not need checksums"; fi
  if needs_has provenance prepare; then pass "provenance needs prepare"; else fail "provenance does not need prepare"; fi
  if needs_has publish-release provenance; then pass "publish-release needs provenance"; else fail "publish-release does not need provenance"; fi
  if needs_has cleanup-orphaned-tag provenance; then pass "cleanup-orphaned-tag lists provenance"; else fail "cleanup-orphaned-tag does not list provenance"; fi
  if needs_has provenance-dmg macos-dmg-attach; then pass "provenance-dmg needs macos-dmg-attach"; else fail "provenance-dmg does not need macos-dmg-attach"; fi
  if grep -qF "needs.provenance.result == 'success'" <<<"$(job_block publish-release)"; then
    pass "publish-release's if: gates on provenance success"
  else
    fail "publish-release's if: does not gate on needs.provenance.result == 'success'"
  fi
  if needs_has provenance macos-app || needs_has provenance macos-dmg-attach; then
    fail "provenance (gates publish) must not need macos-app or macos-dmg-attach (ut-docs#2917)"
  else
    pass "provenance does not need a macOS job"
  fi

  for j in provenance provenance-dmg; do
    block="$(job_block "$j")"
    code="$(code_of "$block")"
    if grep -qE '^    timeout-minutes: [0-9]+' <<<"$code"; then pass "${j} has timeout-minutes"; else fail "${j} has no timeout-minutes"; fi
    if grep -qE '^      id-token: write' <<<"$code"; then pass "${j} has id-token: write"; else fail "${j} lacks id-token: write"; fi
    if grep -qE '^      attestations: write' <<<"$code"; then pass "${j} has attestations: write"; else fail "${j} lacks attestations: write"; fi
    if grep -qE '^    environment:' <<<"$code"; then
      fail "${j} joins an environment: it would hold the release-signing seed next to a Sigstore token (ADR-0181 section 4)"
    else
      pass "${j} has no environment:"
    fi
    if grep -qE '^    runs-on: ubuntu-latest' <<<"$code"; then pass "${j} runs on ubuntu-latest"; else fail "${j} is not on ubuntu-latest"; fi
    # Every `uses:` is a full commit SHA (these jobs hold id-token: write).
    local uses bad
    uses="$(grep -E '^[[:space:]]*-?[[:space:]]*uses:' <<<"$code" || true)"
    if [ -z "$uses" ]; then
      fail "${j} has no uses: lines (parser broken?)"
    else
      bad="$(grep -vE 'uses:[[:space:]]*[A-Za-z0-9_.-]+/[A-Za-z0-9_./-]+@[0-9a-f]{40}([[:space:]]|$)' <<<"$uses" || true)"
      if [ -n "$bad" ]; then
        fail "${j} has a uses: not pinned to a full commit SHA: ${bad}"
      else
        pass "${j}: every uses: is pinned to a full commit SHA"
      fi
    fi
    if grep -qE 'uses:[[:space:]]*actions/attest-build-provenance@[0-9a-f]{40}' <<<"$code"; then
      pass "${j} attests with a pinned actions/attest-build-provenance"
    else
      fail "${j} has no pinned actions/attest-build-provenance step"
    fi
    # Literal $VAR text in release.yml is matched: single quotes are intended.
    # shellcheck disable=SC2016
    if grep -qE '^        run: bash scripts/ci/verify-release-provenance\.sh "\$RUNNER_TEMP/assets" "\$TAG"[[:space:]]*$' <<<"$code"; then
      pass "${j} runs verify-release-provenance.sh as an executable step"
    else
      fail "${j} has no executable 'run: bash scripts/ci/verify-release-provenance.sh \"\$RUNNER_TEMP/assets\" \"\$TAG\"' step"
    fi
    # Literal $VAR text in release.yml is matched: single quotes are intended.
    # shellcheck disable=SC2016
    if grep -qE '^[[:space:]]*bash scripts/ci/check-release-checksums\.sh "\$ASSETS/checksums\.txt" "\$ASSETS"[[:space:]]*$' <<<"$code"; then
      pass "${j} binds the assets to checksums.txt (check-release-checksums.sh)"
    else
      fail "${j} does not run check-release-checksums.sh on what it attests"
    fi
    # The verify step (and every other step) must gate the job: a
    # continue-on-error would let unverified assets be published.
    if grep -qE 'continue-on-error' <<<"$code"; then
      fail "${j} has continue-on-error: a failed attestation or verification would not block the release"
    else
      pass "${j} has no continue-on-error"
    fi
    # `provenance` gates publish: it must not run on a failed/cancelled path.
    if grep -qE '^    if:.*(always\(\)|failure\(\))' <<<"$code"; then
      fail "${j} runs under always()/failure(): it must only run after its inputs succeeded"
    else
      pass "${j} has no if: always()/failure()"
    fi
    # The sha256 binding comparisons, as executable lines.
    # Literal $VAR text in release.yml is matched: single quotes are intended.
    # shellcheck disable=SC2016
    if grep -qE '^[[:space:]]*if \[ -z "\$CHECKSUMS_SHA256" \] \|\| \[ "\$got" != "\$CHECKSUMS_SHA256" \]; then[[:space:]]*$' <<<"$code"; then
      pass "${j} compares checksums.txt with its producing job's checksums_sha256 output"
    else
      fail "${j} lacks the executable checksums_sha256 comparison"
    fi
  done
  # Literal $VAR text in release.yml is matched: single quotes are intended.
  # shellcheck disable=SC2016
  if grep -qE '^[[:space:]]*if \[ -z "\$DMG_SHA256" \] \|\| \[ "\$got" != "\$DMG_SHA256" \]; then[[:space:]]*$' <<<"$(code_of "$(job_block provenance-dmg)")" \
    && grep -qF 'DMG_SHA256: ${{ needs.macos-app.outputs.dmg_sha256 }}' <<<"$(code_of "$(job_block provenance-dmg)")"; then
    pass "provenance-dmg binds the .dmg to macos-app's dmg_sha256"
  else
    fail "provenance-dmg lacks the executable dmg_sha256 comparison against needs.macos-app.outputs.dmg_sha256"
  fi

  # No `write-all` anywhere (it would hand id-token/attestations to every job).
  if grep -qE 'write-all' <<<"$(grep -vE '^[[:space:]]*#' "$WF" || true)"; then
    fail "release.yml uses permissions: write-all"
  else
    pass "release.yml has no write-all"
  fi
  # checksums.txt.sig is bound by a sig_sha256 output, like checksums.txt.
  if grep -qE '^      sig_sha256: \$\{\{ steps\.upload\.outputs\.sig_sha256 \}\}' <<<"$(code_of "$(job_block checksums)")"; then
    pass "checksums exports sig_sha256"
  else
    fail "checksums does not export sig_sha256"
  fi
  # Literal $VAR text in release.yml is matched: single quotes are intended.
  # shellcheck disable=SC2016
  if grep -qF 'SIG_SHA256: ${{ needs.checksums.outputs.sig_sha256 }}' <<<"$(code_of "$(job_block provenance)")" \
    && grep -qE '^[[:space:]]*if \[ "\$got_sig" != "\$SIG_SHA256" \]; then[[:space:]]*$' <<<"$(code_of "$(job_block provenance)")"; then
    pass "provenance binds checksums.txt.sig to checksums' sig_sha256"
  else
    fail "provenance lacks the executable sig_sha256 comparison"
  fi
  # Literal $VAR text in release.yml is matched: single quotes are intended.
  # shellcheck disable=SC2016
  if grep -qF 'SIG_SHA256: ${{ needs.macos-dmg-attach.outputs.sig_sha256 }}' <<<"$(code_of "$(job_block provenance-dmg)")" \
    && grep -qE '^[[:space:]]*if \[ "\$got_sig" != "\$SIG_SHA256" \]; then[[:space:]]*$' <<<"$(code_of "$(job_block provenance-dmg)")"; then
    pass "provenance-dmg binds checksums.txt.sig to macos-dmg-attach's sig_sha256"
  else
    fail "provenance-dmg lacks the executable sig_sha256 comparison"
  fi

  # Permission scope across the whole workflow.
  local att idt top
  att=""
  idt=""
  while IFS= read -r j; do
    [ -n "$j" ] || continue
    code="$(code_of "$(job_block "$j")")"
    if grep -qE '^[[:space:]]+attestations: write' <<<"$code"; then att="${att} ${j}"; fi
    if grep -qE '^[[:space:]]+id-token: write' <<<"$code"; then idt="${idt} ${j}"; fi
  done <<<"$(jobs_of)"
  att="$(tr ' ' '\n' <<<"$att" | sed '/^$/d' | sort | tr '\n' ' ')"
  idt="$(tr ' ' '\n' <<<"$idt" | sed '/^$/d' | sort | tr '\n' ' ')"
  if [ "$att" = "provenance provenance-dmg " ]; then
    pass "attestations: write only in provenance, provenance-dmg"
  else
    fail "attestations: write is in: ${att:-none} (want exactly provenance, provenance-dmg)"
  fi
  if [ "$idt" = "goreleaser provenance provenance-dmg windows-installer " ]; then
    pass "id-token: write only in goreleaser, windows-installer, provenance, provenance-dmg"
  else
    fail "id-token: write is in: ${idt:-none} (want exactly goreleaser, provenance, provenance-dmg, windows-installer)"
  fi
  top="$(awk '/^jobs:/{exit} {print}' "$WF" | sed 's/^[[:space:]]*#.*$//')"
  if grep -qE 'id-token|attestations' <<<"$top"; then
    fail "workflow-level permissions grant id-token or attestations"
  else
    pass "workflow-level permissions grant neither id-token nor attestations"
  fi
}

check_verify_script() {
  local want code
  if [ ! -f "$VERIFY" ]; then
    fail "${VERIFY} is missing"
    return 0
  fi
  # Comments mention every flag in the header: only executable lines count.
  code="$(grep -vE '^[[:space:]]*#' "$VERIFY" || true)"
  for want in 'gh attestation verify' '--repo' '--signer-workflow' '--source-digest' '--deny-self-hosted-runners' \
    'sourceRepositoryRef' 'refs/tags/' 'refs/heads/main' 'drill'; do
    if grep -qF -- "$want" <<<"$code"; then pass "verify script has: ${want}"; else fail "verify script is missing: ${want}"; fi
  done
  if grep -qF -- '--source-ref' <<<"$code"; then
    fail "verify script passes a single --source-ref (breaks the tag-push or dispatch path)"
  else
    pass "verify script passes no single --source-ref"
  fi
}

# ---- behaviour: verify-release-provenance.sh against a stubbed gh ----------
STUBDIR="$(mktemp -d)"
cleanup() { rm -rf "$STUBDIR"; remove_guard_test_sandbox; }
trap cleanup EXIT

cat >"$STUBDIR/gh" <<'STUB'
#!/usr/bin/env bash
# Stub gh: `gh attestation verify <file> <flags> `. It behaves like the real
# thing for the checks that matter: it FAILS unless every ADR-0181 binding flag
# is present with the right value (--repo, --signer-workflow <repo>/.github/
# workflows/release.yml, --source-digest <the sha passed>, --deny-self-hosted-
# runners, --format json), and then fails for an unknown file digest or a wrong
# signer, exactly as verification would. A flag error is also written to
# $GH_STUB_FLAGERR so the test can assert none happened.
# GH_STUB_MODE: good | flaky (fails once per known file, then passes) | fail | wrongref | wrongtag | drill-tamper-passes | drill-signer-passes
# wrongref / wrongtag change only the emitted ref; the drill-*-passes modes weaken only
# the one check they name.
set -euo pipefail
[ "$1" = attestation ] && [ "$2" = verify ] || { echo "unexpected gh call: $*" >&2; exit 2; }
file="$3"; shift 3
signer="" repo="" digest="" deny=0 format=""
while [ $# -gt 0 ]; do
  case "$1" in
    --signer-workflow) signer="$2"; shift ;;
    --repo) repo="$2"; shift ;;
    --source-digest) digest="$2"; shift ;;
    --deny-self-hosted-runners) deny=1 ;;
    --format) format="$2"; shift ;;
    *) echo "stub: unexpected flag $1" >>"$GH_STUB_FLAGERR" ;;
  esac
  shift
done
flagbad() { echo "stub: $1" >>"$GH_STUB_FLAGERR"; echo "stub: $1" >&2; exit 4; }
[ "$repo" = "$GH_STUB_REPO" ] || flagbad "--repo is '${repo}', want ${GH_STUB_REPO}"
[ -n "$signer" ] || flagbad "no --signer-workflow"
[ "$digest" = "$GH_STUB_DIGEST" ] || flagbad "--source-digest is '${digest}', want ${GH_STUB_DIGEST}"
[ "$deny" -eq 1 ] || flagbad "no --deny-self-hosted-runners"
[ "$format" = json ] || flagbad "no --format json"
sha="$(sha256sum "$file" | awk '{print $1}')"
known=0
grep -qx "$sha" "$GH_STUB_GOOD" && known=1
emit() { printf '[{"verificationResult":{"signature":{"certificate":{"sourceRepositoryRef":"%s"}}}}]\n' "$1"; }
ref=refs/tags/v1.2.3
[ "$GH_STUB_MODE" = wrongref ] && ref=refs/heads/evil
[ "$GH_STUB_MODE" = wrongtag ] && ref=refs/tags/v9.9.9
[ "$GH_STUB_MODE" = fail ] && { echo "no attestations found" >&2; exit 1; }
if [ "$GH_STUB_MODE" = flaky ] && [ "$known" -eq 1 ] && [ ! -e "$GH_STUB_STATE/$sha" ]; then
  : >"$GH_STUB_STATE/$sha"; echo "transient: attestation not found yet" >&2; exit 1
fi
if [ "$known" -ne 1 ] && [ "$GH_STUB_MODE" != drill-tamper-passes ]; then
  echo "digest not attested" >&2; exit 1
fi
case "$signer" in
  "$GH_STUB_REPO"/.github/workflows/release.yml) emit "$ref" ;;
  *) if [ "$GH_STUB_MODE" = drill-signer-passes ]; then emit "$ref"; else echo "wrong signer" >&2; exit 1; fi ;;
esac
STUB
chmod +x "$STUBDIR/gh"

ASSETS="$STUBDIR/assets"
mkdir -p "$ASSETS"
printf 'archive bytes' >"$ASSETS/unitill-pos_1.2.3_linux_amd64.tar.gz"
printf 'sums' >"$ASSETS/checksums.txt"
sha256sum "$ASSETS"/* | awk '{print $1}' >"$STUBDIR/good.txt"

run_verify() { # run_verify <mode> -> status; output in $STUBDIR/out
  : >"$STUBDIR/flagerr"
  rm -rf "$STUBDIR/state" && mkdir -p "$STUBDIR/state"
  GH_STUB_STATE="$STUBDIR/state" VERIFY_SLEEP=0 GH_STUB_MODE="$1" GH_STUB_GOOD="$STUBDIR/good.txt" GH_STUB_FLAGERR="$STUBDIR/flagerr" \
    GH_STUB_REPO=acme/till GH_STUB_DIGEST="$DIGEST" GITHUB_REPOSITORY=acme/till PATH="$STUBDIR:$PATH" \
    bash "$VERIFY" "$ASSETS" v1.2.3 "$DIGEST" >"$STUBDIR/out" 2>&1
}
DIGEST=0123456789abcdef0123456789abcdef01234567
# expect_status <mode> <want: 0|1> <label> [<message the output must contain>]
# A failing case must fail on the check it names, not on a stray flag error
# or an unrelated drill: the message is asserted, and no flag error may occur.
expect_status() {
  local st=0
  run_verify "$1" || st=$?
  if [ "$st" -ne "$2" ]; then
    fail "verify script ($3): exit ${st}, want ${2}: $(tr '\n' '|' <"$STUBDIR/out")"
  elif [ -s "$STUBDIR/flagerr" ]; then
    fail "verify script ($3): gh was called with wrong or missing flags: $(tr '\n' '|' <"$STUBDIR/flagerr")"
  elif [ -n "${4:-}" ] && ! grep -qF -- "$4" "$STUBDIR/out"; then
    fail "verify script ($3): output lacks '${4}': $(tr '\n' '|' <"$STUBDIR/out")"
  else
    pass "verify script ($3): exit ${st}${4:+, says: ${4}}"
  fi
}

check_behaviour() {
  if [ ! -f "$VERIFY" ]; then fail "${VERIFY} is missing: cannot run the behaviour checks"; return 0; fi
  expect_status good 0 "all attested (every binding flag passed)" "all 2 file(s) verified"
  expect_status fail 1 "gh verify fails" "no valid attestation for"
  expect_status fail 1 "gh verify fails on every retry" "verify attempt 2/3 failed"
  expect_status flaky 0 "positive verify fails once, then passes (bounded retry)" "all 2 file(s) verified"
  expect_status wrongref 1 "attested from an unexpected ref" "not from refs/tags/v1.2.3 or refs/heads/main"
  expect_status wrongtag 1 "attested from a different tag" "not from refs/tags/v1.2.3 or refs/heads/main (got: refs/tags/v9.9.9)"
  expect_status drill-tamper-passes 1 "tampered copy unexpectedly verifies" "drill failed: a byte-modified copy"
  expect_status drill-signer-passes 1 "wrong signer unexpectedly verifies" "drill failed: checksums.txt passed verification against a wrong --signer-workflow"
  # A file whose digest is not attested must fail even when the others pass.
  printf 'swapped' >"$ASSETS/unitill-pos_1.2.3_windows_amd64.zip"
  expect_status good 1 "one asset has no attestation" "no valid attestation for unitill-pos_1.2.3_windows_amd64.zip"
  rm -f "$ASSETS/unitill-pos_1.2.3_windows_amd64.zip"
  local st=0
  GH_STUB_MODE=good GH_STUB_GOOD="$STUBDIR/good.txt" GITHUB_REPOSITORY=acme/till PATH="$STUBDIR:$PATH" \
    bash "$VERIFY" "$STUBDIR/empty-nonexistent" v1.2.3 abc >"$STUBDIR/out" 2>&1 || st=$?
  if [ "$st" -ne 0 ]; then pass "verify script: missing assets dir fails"; else fail "verify script: missing assets dir passed"; fi
}

# ---- run against the real tree ---------------------------------------------
check_wiring
check_verify_script
check_behaviour

# ---- self-test: the wiring checks must fail on planted bad workflows --------
enter_guard_test_sandbox .github/workflows
REAL_WF="$WF"
real_fails="$fails"
selftest() { # selftest <label> <sed-script>
  local before
  sed -E "$2" "$REAL_WF" >.github/workflows/planted.yml
  if cmp -s "$REAL_WF" .github/workflows/planted.yml; then
    fail "self-test '${1}': the planted edit changed nothing (stale pattern)"
    return 0
  fi
  WF=".github/workflows/planted.yml"
  QUIET=1
  before="$fails"
  check_wiring
  QUIET=0
  WF="$REAL_WF"
  if [ "$fails" -gt "$before" ]; then
    fails="$before"
    pass "self-test: wiring check rejects ${1}"
  else
    fail "self-test: wiring check accepted ${1}"
  fi
}
if [ "$real_fails" -eq 0 ]; then
  selftest "an unpinned action in provenance" '/^  provenance:$/,/^  provenance-dmg:$/ s#(actions/attest-build-provenance)@[0-9a-f]{40}#\1@v4#'
  selftest "an environment on provenance-dmg" '/^  provenance-dmg:$/,/^  cleanup-orphaned-tag:$/ s#^(    timeout-minutes: [0-9]+)$#\1\n    environment: release-signing#'
  selftest "attestations: write on publish-release" '/^  publish-release:$/,/^  macos-dmg-attach:$/ s#^(      actions: write)$#\1\n      attestations: write#'
  selftest "a publish-release that does not need provenance" '/^  publish-release:$/,/^  macos-dmg-attach:$/ s#^(        checksums,)$#\1 x,#; s#provenance,##'
  selftest "continue-on-error on the provenance verify step" '/^  provenance:$/,/^  publish-release:$/ s#^(        run: bash scripts/ci/verify-release-provenance\.sh .*)$#\1\n        continue-on-error: true#'
  # Literal $VAR text in release.yml is matched: single quotes are intended.
  # shellcheck disable=SC2016
  selftest "provenance under always()" '/^  provenance:$/,/^  publish-release:$/ s#^    if: \$\{\{ !cancelled\(\) && needs\.checksums\.result == .success. \}\}$#    if: ${{ always() }}#'
  selftest "write-all at the top level" 's#^jobs:$#permissions: write-all\njobs:#'
  selftest "write-all on the checksums job" '/^  checksums:$/,/^  provenance:$/ s#^(    timeout-minutes: [0-9]+)$#\1\n    permissions: write-all#'
  # Literal $VAR text in release.yml is matched: single quotes are intended.
  # shellcheck disable=SC2016
  selftest "a provenance without the sig_sha256 comparison" '/^  provenance:$/,/^  publish-release:$/ s#\[ "\$got_sig" != "\$SIG_SHA256" \]#false#'
  # Literal $VAR text in release.yml is matched: single quotes are intended.
  # shellcheck disable=SC2016
  selftest "a provenance without the checksums_sha256 comparison" '/^  provenance:$/,/^  publish-release:$/ s#\[ "\$got" != "\$CHECKSUMS_SHA256" \]#false#'
else
  echo "skip - self-tests (the real wiring checks already fail)"
fi
remove_guard_test_sandbox

if [ "$fails" -ne 0 ]; then
  echo "${fails} check(s) failed" >&2
  exit 1
fi
echo "all release provenance checks passed"
