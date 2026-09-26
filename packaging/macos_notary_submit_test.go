package packaging

// ut-docs#2917: v0.25.0 sat in `notarytool submit --wait` (no timeout) for
// 100+ minutes and held the whole release as a draft. notary-args.sh's
// notary_submit_and_wait now submits WITHOUT --wait, records the
// submission id (log + job summary) so a human can `notarytool info <id>`,
// then waits with a bounded `notarytool wait --timeout`, and succeeds only
// on status "Accepted". These tests drive it against a stub `xcrun` on
// PATH, so they run on any OS.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const stubSubmissionID = "2efe2717-52ef-43a5-96dc-0797e4ca1041"

// stubXcrun is a fake `xcrun` whose notarytool behaviour follows STUB_MODE:
// accepted | invalid | timeout | noid. Every call's argv lands in $STUB_LOG.
const stubXcrun = `#!/usr/bin/env bash
printf '%s\n' "$*" >> "$STUB_LOG"
[ "$1" = notarytool ] || exit 99
case "$2" in
  submit)
    if [ "$STUB_MODE" = noid ]; then echo '{"message":"upload failed"}'; exit 0; fi
    echo '{"id":"` + stubSubmissionID + `","message":"Successfully uploaded file","path":"x.dmg"}'
    ;;
  wait)
    case "$STUB_MODE" in
      accepted) echo '{"id":"` + stubSubmissionID + `","message":"Processing complete","status":"Accepted"}' ;;
      invalid)  echo '{"id":"` + stubSubmissionID + `","message":"Processing complete","status":"Invalid"}'; exit 1 ;;
      timeout)  echo 'Error: timed out waiting for submission' >&2; exit 1 ;;
    esac
    ;;
  log) echo '{"issues":[{"message":"stub issue"}]}' ;;
  *) exit 98 ;;
esac
`

type notaryRun struct {
	err     error
	out     string
	calls   string
	summary string
}

func runNotarySubmit(t *testing.T, mode string, extraEnv ...string) notaryRun {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "xcrun"), []byte(stubXcrun), 0o755); err != nil {
		t.Fatal(err)
	}
	logf := filepath.Join(dir, "calls.log")
	sumf := filepath.Join(dir, "summary.md")
	script := `
set -euo pipefail
. packaging/macos/notary-args.sh
notary_args
notary_submit_and_wait /tmp/fake.dmg
`
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = ".."
	cmd.Env = append([]string{
		"PATH=" + dir + string(os.PathListSeparator) + os.Getenv("PATH"),
		"TMPDIR=" + t.TempDir(),
		"STUB_MODE=" + mode,
		"STUB_LOG=" + logf,
		"GITHUB_STEP_SUMMARY=" + sumf,
		"MACOS_NOTARY_KEY_P8=fake-p8",
		"MACOS_NOTARY_KEY_ID=ABCDE12345",
		"MACOS_NOTARY_ISSUER_ID=00000000-1111-2222-3333-444444444444",
	}, extraEnv...)
	out, err := cmd.CombinedOutput()
	calls, _ := os.ReadFile(logf)
	summary, _ := os.ReadFile(sumf)
	return notaryRun{err: err, out: string(out), calls: string(calls), summary: string(summary)}
}

func callWith(calls, sub string) string {
	for _, l := range strings.Split(calls, "\n") {
		if strings.HasPrefix(l, "notarytool "+sub+" ") {
			return l
		}
	}
	return ""
}

func TestNotarySubmitAcceptedIsBoundedAndRecordsID(t *testing.T) {
	r := runNotarySubmit(t, "accepted")
	if r.err != nil {
		t.Fatalf("accepted submission failed: %v\n%s", r.err, r.out)
	}
	submit := callWith(r.calls, "submit")
	if submit == "" || strings.Contains(submit, "--wait") {
		t.Fatalf("submit must run once WITHOUT an unbounded --wait; calls:\n%s", r.calls)
	}
	wait := callWith(r.calls, "wait")
	if !strings.Contains(wait, stubSubmissionID) || !strings.Contains(wait, "--timeout 45m") {
		t.Fatalf("wait must target the submission id with --timeout 45m; calls:\n%s", r.calls)
	}
	if !strings.Contains(r.summary, stubSubmissionID) || !strings.Contains(r.summary, "notarytool info") {
		t.Fatalf("job summary lacks the submission id / info hint:\n%s", r.summary)
	}
	if !strings.Contains(r.out, stubSubmissionID) {
		t.Fatalf("log lacks the submission id:\n%s", r.out)
	}
}

func TestNotarySubmitTimeoutOverride(t *testing.T) {
	r := runNotarySubmit(t, "accepted", "NOTARY_TIMEOUT=10m")
	if r.err != nil {
		t.Fatalf("%v\n%s", r.err, r.out)
	}
	if !strings.Contains(callWith(r.calls, "wait"), "--timeout 10m") {
		t.Fatalf("NOTARY_TIMEOUT not honoured; calls:\n%s", r.calls)
	}
}

func TestNotarySubmitRejectsBadTimeout(t *testing.T) {
	r := runNotarySubmit(t, "accepted", "NOTARY_TIMEOUT=45m; rm -rf x")
	if r.err == nil {
		t.Fatalf("a malformed NOTARY_TIMEOUT must fail:\n%s", r.out)
	}
	if callWith(r.calls, "submit") != "" {
		t.Fatalf("nothing may be submitted with a malformed timeout; calls:\n%s", r.calls)
	}
}

func TestNotarySubmitFailsClosed(t *testing.T) {
	for _, mode := range []string{"invalid", "timeout"} {
		t.Run(mode, func(t *testing.T) {
			r := runNotarySubmit(t, mode)
			if r.err == nil {
				t.Fatalf("%s must fail the build (never ship an un-notarized dmg):\n%s", mode, r.out)
			}
			if !strings.Contains(r.summary, stubSubmissionID) {
				t.Fatalf("id must be in the summary even on failure:\n%s", r.summary)
			}
			if callWith(r.calls, "log") == "" {
				t.Fatalf("a failed notarization should fetch the notary log; calls:\n%s", r.calls)
			}
		})
	}
}

func TestNotarySubmitNoIDFails(t *testing.T) {
	r := runNotarySubmit(t, "noid")
	if r.err == nil {
		t.Fatalf("a submit with no id must fail:\n%s", r.out)
	}
	if callWith(r.calls, "wait") != "" {
		t.Fatalf("must not wait on an empty id; calls:\n%s", r.calls)
	}
}

// make-dmg.sh must notarize through the bounded helper, never with a bare
// `notarytool submit … --wait` (the v0.25.0 hang).
func TestMakeDmgUsesBoundedNotarization(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("macos", "make-dmg.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "notary_submit_and_wait") {
		t.Fatal("make-dmg.sh does not notarize via notary_submit_and_wait")
	}
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "notarytool submit") && !strings.HasPrefix(strings.TrimSpace(line), "#") {
			t.Fatalf("make-dmg.sh still calls notarytool submit directly: %q", strings.TrimSpace(line))
		}
	}
}

// ut-docs#2870 (v0.25.0 run 36221557677): Apple accepted the notarization
// but `spctl --assess --type open --context context:primary-signature`
// rejected the .dmg with "no usable signature" — a disk image must itself
// be code-signed with the Developer ID (Apple's distribution flow: create
// .dmg → codesign it → notarize → staple → assess). Pin that the .dmg is
// signed after hdiutil create and before notarization.
func TestMakeDmgSignsTheDiskImageBeforeNotarizing(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("macos", "make-dmg.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	create := strings.Index(s, "hdiutil create")
	sign := strings.Index(s, `codesign --force --timestamp --sign "$MACOS_SIGN_IDENTITY" "$DMG"`)
	notarize := strings.Index(s, `notary_submit_and_wait "$DMG"`)
	if create < 0 || notarize < 0 {
		t.Fatal("make-dmg.sh no longer creates and notarizes the .dmg as expected")
	}
	if sign < 0 {
		t.Fatal("make-dmg.sh never code-signs the .dmg; Gatekeeper rejects an unsigned disk image")
	}
	if sign < create || sign > notarize {
		t.Fatal("the .dmg must be signed after hdiutil create and before notarization")
	}
}
