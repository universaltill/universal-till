package pages

import (
	"testing"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/updates"
)

// Tester proposal (ut-docs#6551, ADR-0194 §2.3): a store build never prefetches.
func TestAutoUpdateTick_AndroidStoreBuildNeverPrefetches(t *testing.T) {
	t.Cleanup(buildinfo.OverrideChannelForTest(buildinfo.ChannelPlay))
	dp := dueAndroidDeps(t)
	fp, applyCalls := stubAndroidTick(t, updates.Status{Available: true}, updates.Status{Available: true}, true)
	autoUpdateTick(t.Context(), dp, tickNow)
	if fp.calls != 0 || *applyCalls != 0 {
		t.Fatalf("store build: prefetch=%d apply=%d, want 0/0", fp.calls, *applyCalls)
	}
}
