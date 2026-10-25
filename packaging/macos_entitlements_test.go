package packaging

// ut-docs#5023: the Developer ID build signs with the hardened runtime, and
// unitill-pos runs wazero's compiler, which writes machine code at runtime
// with mmap + mprotect(RX) (not MAP_JIT). Without
// com.apple.security.cs.allow-unsigned-executable-memory the kernel kills the
// till server on start (EXC_BAD_ACCESS / SIGKILL "Code Signature Invalid") and
// the app opens to an empty window. allow-jit is NOT enough for wazero. Pin
// the entitlement, that it stays the only one (least privilege), and that the
// bundle pass does not strip it again (codesign --deep re-signs nested code
// without its entitlements).

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

const wantEntitlement = "com.apple.security.cs.allow-unsigned-executable-memory"

// buildScriptLines is build-app.sh without comment lines, so a commented-out or
// quoted-in-a-comment codesign call never satisfies a check.
func buildScriptLines(t *testing.T) []string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("macos", "build-app.sh"))
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, l := range strings.Split(string(b), "\n") {
		if tl := strings.TrimSpace(l); tl != "" && !strings.HasPrefix(tl, "#") {
			out = append(out, tl)
		}
	}
	return out
}

// signLine returns the one real signing call (not `codesign -d` / `--verify`)
// whose target is the quoted path.
func signLine(t *testing.T, target string) string {
	t.Helper()
	var found []string
	for _, l := range buildScriptLines(t) {
		if strings.HasPrefix(l, `codesign "${SIGN_ARGS[@]}"`) && strings.HasSuffix(l, target) {
			found = append(found, l)
		}
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one signing call ending %s in build-app.sh, got %d: %v", target, len(found), found)
	}
	return found[0]
}

func TestMacosPosBinaryIsSignedWithTheEntitlements(t *testing.T) {
	l := signLine(t, `"$APP/Contents/MacOS/unitill-pos"`)
	if !strings.Contains(l, `--entitlements "$ENTITLEMENTS"`) {
		t.Fatalf("the unitill-pos signing call must pass --entitlements: %s", l)
	}
	var assigned bool
	for _, c := range buildScriptLines(t) {
		if c == `ENTITLEMENTS="$REPO_ROOT/packaging/macos/entitlements.plist"` {
			assigned = true
		}
	}
	if !assigned {
		t.Fatal(`ENTITLEMENTS must be "$REPO_ROOT/packaging/macos/entitlements.plist"`)
	}
}

func TestMacosBundlePassDoesNotStripEntitlements(t *testing.T) {
	if l := signLine(t, `"$APP"`); strings.Contains(l, "--deep") {
		t.Fatalf("the bundle signing call must not use --deep (it drops the inner binary's entitlements): %s", l)
	}
	// The build must also fail, not warn, if the entitlement is gone or the
	// bundle signature is invalid.
	joined := strings.Join(buildScriptLines(t), "\n")
	if !strings.Contains(joined, wantEntitlement+`*"[Bool] true"*`) {
		t.Fatal("build-app.sh no longer checks the signed unitill-pos for the entitlement set to true")
	}
	if !regexp.MustCompile(`codesign --verify --deep --strict "\$APP" \|\| \{`).MatchString(joined) {
		t.Fatal("the bundle signature verification must fail the build (|| exit), not sit left of &&")
	}
}

func TestMacosEntitlementsGrantExactlyOneEntitlement(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("macos", "entitlements.plist"))
	if err != nil {
		t.Fatal(err)
	}
	noComments := regexp.MustCompile(`(?s)<!--.*?-->`).ReplaceAllString(string(b), "")
	pairs := regexp.MustCompile(`<key>([^<]+)</key>\s*<(true|false)/>`).FindAllStringSubmatch(noComments, -1)
	if len(pairs) != 1 || pairs[0][1] != wantEntitlement || pairs[0][2] != "true" {
		t.Fatalf("entitlements.plist must grant exactly %s=true, got %v", wantEntitlement, pairs)
	}
	if n := strings.Count(noComments, "<key>"); n != 1 {
		t.Fatalf("entitlements.plist has %d keys, want 1", n)
	}
}
