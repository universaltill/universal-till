package pages

import (
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/cloudsync"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// Per-field ver/base_ver and the tier_mismatch refusal on set_till_setting
// (shop-config design 4.2/4.3, group U3). kiosk.idle_reset_seconds is a
// normal-tier, remote-writable registry key.

func ver(n int64) *int64 { return &n }

func reasonKey(err error) string {
	var re *cloudsync.ReasonError
	if errors.As(err, &re) {
		return re.Key
	}
	return ""
}

func TestCloudSetTillSetting_VerApplies_ReportShowsVerOriginCloud(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	key := common.KeyKioskIdleReset
	if _, err := cloudSetTillSettingCond(ctx, dp, nil, key, "30", nil, cloudsync.TillSettingCond{Ver: ver(4), BaseVer: ver(1), Tier: data.SettingTierNormal}); err != nil {
		t.Fatalf("conditional write: %v", err)
	}
	if v, _, _ := dp.Settings.Get(ctx, key); v != "30" {
		t.Fatalf("stored %q, want 30", v)
	}
	rep := remoteTillSettingsMetaReport(ctx, dp)
	if got := rep[key]; got.Ver != 4 || got.Origin != "cloud" || got.At == "" {
		t.Fatalf("meta report for %s = %+v, want ver 4 origin cloud with a time", key, got)
	}
}

func TestCloudSetTillSetting_BaseVerOlderThanLocal_conflict_NothingWritten(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	key := common.KeyKioskIdleReset
	if err := dp.Settings.Set(ctx, key, "20"); err != nil { // a till-side edit: ver 2
		t.Fatal(err)
	}
	_, err := cloudSetTillSettingCond(ctx, dp, nil, key, "99", nil, cloudsync.TillSettingCond{Ver: ver(7), BaseVer: ver(1)})
	if reasonKey(err) != cloudsync.ReasonSettingConflict || reasonKey(err) != "conflict" {
		t.Fatalf("err = %v (key %q), want reason conflict", err, reasonKey(err))
	}
	if v, _, _ := dp.Settings.Get(ctx, key); v != "20" {
		t.Fatalf("a conflict wrote %q, want 20 untouched", v)
	}
}

func TestCloudSetTillSetting_VerNotNewer_stale_version(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	key := common.KeyKioskIdleReset
	_ = dp.Settings.Set(ctx, key, "20") // ver 2
	_, err := cloudSetTillSettingCond(ctx, dp, nil, key, "99", nil, cloudsync.TillSettingCond{Ver: ver(2), BaseVer: ver(2)})
	if reasonKey(err) != "stale_version" {
		t.Fatalf("err = %v, want reason stale_version", err)
	}
}

func TestCloudSetTillSetting_TierMismatch_Refused_NothingWritten(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	key := common.KeyKioskIdleReset // normal on the till
	_, err := cloudSetTillSettingCond(ctx, dp, nil, key, "30", nil, cloudsync.TillSettingCond{Tier: data.SettingTierSensitive})
	if reasonKey(err) != "tier_mismatch" {
		t.Fatalf("err = %v, want reason tier_mismatch", err)
	}
	if _, found, _ := dp.Settings.Get(ctx, key); found {
		t.Fatal("a tier mismatch wrote the value")
	}
	// the matching tier, and an absent tier (older cloud), both apply
	for _, tier := range []string{data.SettingTierNormal, ""} {
		if _, err := cloudSetTillSettingCond(ctx, dp, nil, key, "30", nil, cloudsync.TillSettingCond{Tier: tier}); err != nil {
			t.Fatalf("tier %q refused: %v", tier, err)
		}
	}
}

func TestCloudSetTillSetting_NoCond_StillUnconditional(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	key := common.KeyKioskIdleReset
	_ = dp.Settings.Set(ctx, key, "20")
	if _, err := cloudSetTillSetting(ctx, dp, nil, key, "45", nil); err != nil {
		t.Fatalf("an older cloud's directive: %v", err)
	}
	if v, _, _ := dp.Settings.Get(ctx, key); v != "45" {
		t.Fatalf("stored %q, want 45", v)
	}
	if m := remoteTillSettingsMetaReport(ctx, dp)[key]; m.Ver != 3 || m.Origin != "cloud" {
		t.Fatalf("meta = %+v, want ver 3 origin cloud", m)
	}
}

func TestTillSettingsMetaReport_OnlyRegistryKeys_SecretsNeverCarryValues(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	rep := remoteTillSettingsMetaReport(ctx, dp)
	for k := range rep {
		if _, ok := data.LookupAgentSetting(k); !ok {
			t.Errorf("meta report carries %q, which is not a registry key", k)
		}
	}
}
