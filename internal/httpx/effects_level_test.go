package httpx

import "testing"

// ADR-0119 §2: the resolved visual effects level is a process global, the
// same fail-safe shape as InitOSKMode — anything but a resolved level
// (including "auto", which is resolved before it gets here) renders Full.
func TestInitEffectsLevelValidatesInput(t *testing.T) {
	defer InitEffectsLevel("full")
	for _, lvl := range []string{"full", "balanced", "light"} {
		InitEffectsLevel(lvl)
		if got := effectsLevelVal(); got != lvl {
			t.Errorf("effectsLevelVal after %q = %q", lvl, got)
		}
	}
	for _, bad := range []string{"", "auto", "fx-light", "LIGHT", "none"} {
		InitEffectsLevel("light")
		InitEffectsLevel(bad)
		if got := effectsLevelVal(); got != "full" {
			t.Errorf("effectsLevelVal after invalid %q = %q, want full", bad, got)
		}
	}
}

func TestFuncsFor_ExposesFxLevel(t *testing.T) {
	defer InitEffectsLevel("full")
	InitEffectsLevel("balanced")
	fn, ok := FuncsFor("en")["fxlevel"].(func() string)
	if !ok {
		t.Fatalf("fxlevel func missing or wrong shape: %T", FuncsFor("en")["fxlevel"])
	}
	if fn() != "balanced" {
		t.Errorf("fxlevel() = %q, want balanced", fn())
	}
}

// A level change must force exactly one full load on the next boosted
// navigation (ADR-0119 §2): the <html> class is outside the swapped region.
func TestShellSignature_ChangesWithEffectsLevel(t *testing.T) {
	defer InitEffectsLevel("full")
	InitEffectsLevel("full")
	full := ShellSignature("en", "default")
	InitEffectsLevel("light")
	light := ShellSignature("en", "default")
	InitEffectsLevel("balanced")
	balanced := ShellSignature("en", "default")
	if full == light || full == balanced || light == balanced {
		t.Errorf("signature does not follow the effects level: full=%s light=%s balanced=%s", full, light, balanced)
	}
}
