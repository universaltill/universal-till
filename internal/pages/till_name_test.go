package pages

import "testing"

// tillNameOrDefault reads this (primary) till's own name — distinct from a
// replica's own sync.till_name — defaulting when unset (ut-docs#396).
func TestTillNameOrDefault(t *testing.T) {
	dp, _ := newSeededPagesDeps(t)
	ctx := t.Context()

	if got := tillNameOrDefault(ctx, dp, "en"); got != "Till 1" {
		t.Fatalf("unset till.name default = %q, want %q", got, "Till 1")
	}

	if err := dp.Settings.Set(ctx, "till.name", "Front Counter"); err != nil {
		t.Fatalf("set till.name: %v", err)
	}
	if got := tillNameOrDefault(ctx, dp, "en"); got != "Front Counter" {
		t.Fatalf("set till.name = %q, want %q", got, "Front Counter")
	}

	// Blank/whitespace-only is treated the same as unset.
	if err := dp.Settings.Set(ctx, "till.name", "   "); err != nil {
		t.Fatalf("set blank till.name: %v", err)
	}
	if got := tillNameOrDefault(ctx, dp, "en"); got != "Till 1" {
		t.Fatalf("blank till.name = %q, want default %q", got, "Till 1")
	}
}

// The default must go through T, not a bare English literal (independent
// review, ut-docs#396) — otherwise an upgraded install with no till.name set
// shows "Till 1" in Latin script on an RTL Farsi/Arabic till, contradicting
// the wizard's own translated prefill and the translated manual topics.
func TestTillNameOrDefault_TranslatesPerLocale(t *testing.T) {
	dp, _ := newSeededPagesDeps(t)
	ctx := t.Context()

	if got := tillNameOrDefault(ctx, dp, "fa"); got != "صندوق ۱" {
		t.Fatalf("unset till.name default (fa) = %q, want %q", got, "صندوق ۱")
	}
	if got := tillNameOrDefault(ctx, dp, "ar"); got != "الصندوق 1" {
		t.Fatalf("unset till.name default (ar) = %q, want %q", got, "الصندوق 1")
	}

	// A set name is never translated, in any locale.
	if err := dp.Settings.Set(ctx, "till.name", "Front Counter"); err != nil {
		t.Fatalf("set till.name: %v", err)
	}
	if got := tillNameOrDefault(ctx, dp, "fa"); got != "Front Counter" {
		t.Fatalf("set till.name (fa locale) = %q, want %q", got, "Front Counter")
	}
}
