package pages

import "testing"

// ut-docs#6613: cloudsync cuts an auto-registered unpaid till's report to an
// allow-list. The facts the notice promises ("country, language") must still
// come out of the real hook under the key names the allow-list uses, or the
// minimal report would silently lose them.
func TestCheckinHookProvidesAllowListedFactKeys(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if err := dp.Settings.Set(ctx, "store.country", "GB"); err != nil {
		t.Fatal(err)
	}
	extra := buildCloudHooks(dp, nil).DeviceExtra(ctx)
	for _, k := range []string{"locale", "country"} {
		if _, ok := extra[k]; !ok {
			t.Errorf("DeviceExtra has no %q key", k)
		}
	}
}
