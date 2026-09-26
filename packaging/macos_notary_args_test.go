package packaging

// ut-docs#2870: the release notarizes the macOS .dmg with an App Store
// Connect API key (scoped, revocable, not a personal Apple ID password).
// packaging/macos/notary-args.sh picks the notarytool credentials; these
// tests run it under bash with each credential set and check what it
// chose, that the .p8 lands in a private temp file (never argv), and that
// the file is gone after cleanup.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// runNotaryArgs sources notary-args.sh with env, calls notary_args, prints
// the chosen argv (one per line), the key file's mode and whether it still
// exists after notary_cleanup.
func runNotaryArgs(t *testing.T, env map[string]string) string {
	t.Helper()
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skip("bash not available")
	}
	script := `
set -euo pipefail
. packaging/macos/notary-args.sh
notary_args
printf 'ARG:%s\n' "${NOTARY[@]+"${NOTARY[@]}"}"
if [ -n "${NOTARY_KEYFILE:-}" ]; then
  printf 'KEYMODE:%s\n' "$(stat -c %a "$NOTARY_KEYFILE" 2>/dev/null || stat -f %Lp "$NOTARY_KEYFILE")"
  printf 'KEYBODY:%s\n' "$(cat "$NOTARY_KEYFILE")"
  kf="$NOTARY_KEYFILE"
  notary_cleanup
  [ -e "$kf" ] && echo "KEYLEFT:yes" || echo "KEYLEFT:no"
fi
`
	cmd := exec.Command("bash", "-c", script)
	cmd.Dir = ".."
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "TMPDIR=" + t.TempDir()}
	for k, v := range env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("notary-args.sh: %v\n%s", err, out)
	}
	return string(out)
}

func TestNotaryArgsPrefersAPIKey(t *testing.T) {
	out := runNotaryArgs(t, map[string]string{
		"MACOS_NOTARY_KEY_P8":    "-----BEGIN PRIVATE KEY-----\nfake\n-----END PRIVATE KEY-----",
		"MACOS_NOTARY_KEY_ID":    "ABCDE12345",
		"MACOS_NOTARY_ISSUER_ID": "00000000-1111-2222-3333-444444444444",
		// Apple ID creds also present: the API key must win.
		"MACOS_NOTARY_APPLE_ID": "someone@example.com",
		"MACOS_NOTARY_TEAM_ID":  "TEAM123456",
		"MACOS_NOTARY_PASSWORD": "app-specific",
	})
	for _, want := range []string{"ARG:--key-id", "ARG:ABCDE12345", "ARG:--issuer", "ARG:00000000-1111-2222-3333-444444444444", "ARG:--key", "KEYMODE:600", "KEYLEFT:no"} {
		if !strings.Contains(out, want+"\n") {
			t.Fatalf("missing %q in:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "KEYBODY:-----BEGIN PRIVATE KEY-----") {
		t.Fatalf("key file does not hold the .p8 body:\n%s", out)
	}
	if strings.Contains(out, "--apple-id") || strings.Contains(out, "app-specific") {
		t.Fatalf("Apple ID credentials used although an API key is set:\n%s", out)
	}
	// The key material itself must never be an argument.
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "ARG:") && strings.Contains(line, "PRIVATE KEY") {
			t.Fatalf("private key passed on argv: %q", line)
		}
	}
}

func TestNotaryArgsFallsBackToAppleID(t *testing.T) {
	out := runNotaryArgs(t, map[string]string{
		"MACOS_NOTARY_APPLE_ID": "someone@example.com",
		"MACOS_NOTARY_TEAM_ID":  "TEAM123456",
		"MACOS_NOTARY_PASSWORD": "app-specific",
	})
	if !strings.Contains(out, "ARG:--apple-id\n") || strings.Contains(out, "ARG:--key\n") {
		t.Fatalf("want the Apple ID path:\n%s", out)
	}
}

func TestNotaryArgsIncompleteAPIKeyIsNotUsed(t *testing.T) {
	out := runNotaryArgs(t, map[string]string{
		"MACOS_NOTARY_KEY_P8": "-----BEGIN PRIVATE KEY-----\nfake\n-----END PRIVATE KEY-----",
		"MACOS_NOTARY_KEY_ID": "ABCDE12345",
		// no issuer
	})
	if strings.Contains(out, "ARG:--key") || strings.Contains(out, "KEYMODE:") {
		t.Fatalf("incomplete API key credentials must not be used:\n%s", out)
	}
}

func TestNotaryArgsNothingConfigured(t *testing.T) {
	out := runNotaryArgs(t, nil)
	if strings.Contains(out, "ARG:-") {
		t.Fatalf("no credentials should give no notary args:\n%s", out)
	}
}

// make-dmg.sh must use the helper, and must not swallow a failed Gatekeeper
// assessment once it has notarized (the old `spctl … || true`).
func TestMakeDmgUsesNotaryHelperAndFailsClosed(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("macos", "make-dmg.sh"))
	if err != nil {
		t.Fatal(err)
	}
	s := string(b)
	if !strings.Contains(s, "notary-args.sh") || !strings.Contains(s, "notary_args") {
		t.Fatal("make-dmg.sh does not source notary-args.sh / call notary_args")
	}
	for _, line := range strings.Split(s, "\n") {
		if strings.Contains(line, "spctl") && strings.Contains(line, "|| true") {
			t.Fatalf("Gatekeeper assessment is swallowed: %q", strings.TrimSpace(line))
		}
	}
}
