package buildinfo

import "testing"

// ADR-0194 §2.3 binding table, one row per channel.
func TestChannelPredicates_Table(t *testing.T) {
	cases := []struct {
		ch                 Channel
		plan, link, update bool
	}{
		{ChannelSideload, true, true, true},
		{ChannelPlay, false, false, false},
		{ChannelAppStore, false, false, false},
		{ChannelDirect, true, true, true},
	}
	for _, c := range cases {
		if got := c.ch.PlanPurchaseAllowed(); got != c.plan {
			t.Errorf("%s PlanPurchaseAllowed = %v, want %v", c.ch, got, c.plan)
		}
		if got := c.ch.AccountLinkOutAllowed(); got != c.link {
			t.Errorf("%s AccountLinkOutAllowed = %v, want %v", c.ch, got, c.link)
		}
		if got := c.ch.InAppUpdateAllowed(); got != c.update {
			t.Errorf("%s InAppUpdateAllowed = %v, want %v", c.ch, got, c.update)
		}
	}
}

func TestParseChannel_UnknownFailsClosedToAppStore(t *testing.T) {
	for in, want := range map[string]Channel{
		"sideload": ChannelSideload, "play": ChannelPlay, "appstore": ChannelAppStore, "direct": ChannelDirect,
		"": ChannelAppStore, "Play": ChannelAppStore, "microsoft-store": ChannelAppStore, " direct": ChannelAppStore, "ios": ChannelAppStore,
	} {
		if got := ParseChannel(in); got != want {
			t.Errorf("ParseChannel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestCurrentChannel_ReadsStampedValueFailClosed(t *testing.T) {
	t.Cleanup(resetChannelForTest)
	for stamp, want := range map[string]Channel{"play": ChannelPlay, "direct": ChannelDirect, "bogus": ChannelAppStore, "": ChannelAppStore} {
		resetChannelForTest()
		stamped = stamp
		if got := CurrentChannel(); got != want {
			t.Errorf("stamp %q: CurrentChannel = %q, want %q", stamp, got, want)
		}
	}
}

func TestSetChannelOnce(t *testing.T) {
	t.Cleanup(resetChannelForTest)
	resetChannelForTest()
	if !SetChannelOnce("play") {
		t.Fatal("first set refused")
	}
	if CurrentChannel() != ChannelPlay {
		t.Fatalf("got %q", CurrentChannel())
	}
	if SetChannelOnce("direct") {
		t.Fatal("second set must be refused")
	}
	if CurrentChannel() != ChannelPlay {
		t.Fatalf("channel changed after second set: %q", CurrentChannel())
	}
}

func TestSetChannelOnce_UnknownStoresFailClosed(t *testing.T) {
	t.Cleanup(resetChannelForTest)
	resetChannelForTest()
	SetChannelOnce("nonsense")
	if CurrentChannel() != ChannelAppStore {
		t.Fatalf("got %q", CurrentChannel())
	}
}

func TestMobileBuildWithoutChannelFailsClosed(t *testing.T) {
	t.Cleanup(resetChannelForTest)
	resetChannelForTest()
	stamped = "direct" // an AAR is never ldflags-stamped; a stale default must not open purchase
	RequireExplicitChannel()
	if CurrentChannel() != ChannelAppStore {
		t.Fatalf("got %q", CurrentChannel())
	}
	if !SetChannelOnce("play") || CurrentChannel() != ChannelPlay {
		t.Fatalf("explicit set after RequireExplicitChannel failed: %q", CurrentChannel())
	}
}

func TestOverrideChannelForTest(t *testing.T) {
	restore := OverrideChannelForTest(ChannelPlay)
	if CurrentChannel() != ChannelPlay {
		t.Fatal("override not applied")
	}
	restore()
	if CurrentChannel() != ParseChannel(stamped) {
		t.Fatal("restore failed")
	}
}

func resetChannelForTest() {
	chMu.Lock()
	chSet, chExplicit, chValue, stamped = false, false, "", "direct"
	chMu.Unlock()
}

// ADR-0194 / Apple 2.5.2: the App Store build never downloads marketplace
// code (wasm/go) plugins; every other channel does.
func TestCodePluginInstallAllowed_Table(t *testing.T) {
	for ch, want := range map[Channel]bool{
		ChannelSideload: true,
		ChannelPlay:     true,
		ChannelAppStore: false,
		ChannelDirect:   true,
	} {
		if got := ch.CodePluginInstallAllowed(); got != want {
			t.Errorf("%s CodePluginInstallAllowed = %v, want %v", ch, got, want)
		}
	}
	if ParseChannel("bogus").CodePluginInstallAllowed() {
		t.Error("an unknown channel must fail closed to no code plugin install")
	}
}
