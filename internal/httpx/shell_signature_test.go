package httpx

import "testing"

// ut-docs#2224 / ADR-0098: the shell signature gates every boosted swap —
// a response whose <meta name="ut-shell"> differs from the live document's
// is loaded as a full document instead. So anything that changes what
// <head> would render must change the signature.
func TestShellSignature_ChangesWithThemeLocaleAndDirection(t *testing.T) {
	base := ShellSignature("en", "default")
	if base == "" || len(base) > 32 {
		t.Fatalf("signature %q: want a short non-empty token", base)
	}
	if ShellSignature("en", "default") != base {
		t.Fatal("signature is not stable across calls")
	}
	if ShellSignature("en", "dark") == base {
		t.Error("theme change did not change the signature")
	}
	if ShellSignature("de", "default") == base {
		t.Error("locale change did not change the signature")
	}
	if ShellSignature("fa", "default") == base {
		t.Error("RTL locale did not change the signature")
	}
}

func TestShellSignature_ChangesWithAssetVersion(t *testing.T) {
	base := ShellSignature("en", "default")
	old := headAssetVersion
	headAssetVersion = func(rel string) string { return "changed-" + rel }
	t.Cleanup(func() { headAssetVersion = old })
	if ShellSignature("en", "default") == base {
		t.Error("an asset version change did not change the signature")
	}
}

func TestFuncsFor_ExposesShellSig(t *testing.T) {
	f := FuncsFor("de")
	fn, ok := f["shellsig"].(func(any) string)
	if !ok {
		t.Fatalf("shellsig func missing or wrong shape: %T", f["shellsig"])
	}
	if fn("dark") != ShellSignature("de", "dark") {
		t.Error("template shellsig does not match ShellSignature for the request locale")
	}
	// RenderError (every 403/404/500 page) renders base.html with no .theme
	// at all — a nil must render the default signature, never abort the
	// template mid-<head> (independent review finding, 2026-09-17).
	if fn(nil) != ShellSignature("de", "") {
		t.Error("shellsig(nil) must equal the default-theme signature")
	}
}
