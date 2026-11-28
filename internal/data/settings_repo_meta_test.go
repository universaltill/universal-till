package data

import (
	"context"
	"errors"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/testsupport"
)

// Per-field ver/origin for core settings (design-B 4.2/4.3, group U3).
// kiosk.idle_reset_seconds is a registry key; "pos.test.unregistered" is not.

const metaKey = "kiosk.idle_reset_seconds"

func metaRepo(t *testing.T) *SettingsRepo {
	t.Helper()
	dbh, err := db.Open(testsupport.MigratedDBFile(t, "meta.db"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(func() { dbh.Close() })
	return NewSettingsRepo(dbh.DB)
}

func i64(n int64) *int64 { return &n }

func TestSettingMeta_UnwrittenKeyReportsVer1OriginTill(t *testing.T) {
	r := metaRepo(t)
	m, err := r.SettingMeta(context.Background(), metaKey)
	if err != nil || m.Ver != 1 || m.Origin != SettingOriginTill {
		t.Fatalf("meta = %+v err=%v, want ver 1 origin till", m, err)
	}
}

func TestSettingMeta_TillEditBumpsVerOriginTill(t *testing.T) {
	r, ctx := metaRepo(t), context.Background()
	if err := r.Set(ctx, metaKey, "30"); err != nil {
		t.Fatal(err)
	}
	m, _ := r.SettingMeta(ctx, metaKey)
	if m.Ver != 2 || m.Origin != SettingOriginTill || m.At.IsZero() {
		t.Fatalf("after first set = %+v, want ver 2 origin till with a time", m)
	}
	if err := r.Set(ctx, metaKey, "30"); err != nil { // same value: no new version
		t.Fatal(err)
	}
	if m, _ = r.SettingMeta(ctx, metaKey); m.Ver != 2 {
		t.Fatalf("unchanged value bumped ver to %d, want 2", m.Ver)
	}
	if err := r.SetMany(ctx, map[string]string{metaKey: "45"}); err != nil {
		t.Fatal(err)
	}
	if m, _ = r.SettingMeta(ctx, metaKey); m.Ver != 3 {
		t.Fatalf("SetMany change ver = %d, want 3", m.Ver)
	}
}

func TestSettingMeta_UnregisteredKeyHasNoMetaRow(t *testing.T) {
	r, ctx := metaRepo(t), context.Background()
	if err := r.Set(ctx, "pos.test.unregistered", "x"); err != nil {
		t.Fatal(err)
	}
	all, err := r.SettingMetaAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := all["pos.test.unregistered"]; ok {
		t.Fatal("an unregistered key got a setting_meta row")
	}
}

func TestSetFromCloud_AppliesNewerVerOriginCloud(t *testing.T) {
	r, ctx := metaRepo(t), context.Background()
	if err := r.SetFromCloud(ctx, metaKey, "60", i64(5), i64(1)); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := r.Get(ctx, metaKey); v != "60" {
		t.Fatalf("value = %q, want 60", v)
	}
	if m, _ := r.SettingMeta(ctx, metaKey); m.Ver != 5 || m.Origin != SettingOriginCloud {
		t.Fatalf("meta = %+v, want ver 5 origin cloud", m)
	}
}

func TestSetFromCloud_BaseVerOlderThanLocal_conflict_NothingWritten(t *testing.T) {
	r, ctx := metaRepo(t), context.Background()
	_ = r.Set(ctx, metaKey, "30") // local ver 2
	err := r.SetFromCloud(ctx, metaKey, "60", i64(9), i64(1))
	if !errors.Is(err, ErrSettingConflict) {
		t.Fatalf("err = %v, want ErrSettingConflict", err)
	}
	if v, _, _ := r.Get(ctx, metaKey); v != "30" {
		t.Fatalf("value = %q after conflict, want 30 untouched", v)
	}
	if m, _ := r.SettingMeta(ctx, metaKey); m.Ver != 2 || m.Origin != SettingOriginTill {
		t.Fatalf("meta changed by a refused write: %+v", m)
	}
}

func TestSetFromCloud_VerNotNewer_stale_version(t *testing.T) {
	r, ctx := metaRepo(t), context.Background()
	_ = r.SetFromCloud(ctx, metaKey, "60", i64(5), i64(1))
	err := r.SetFromCloud(ctx, metaKey, "70", i64(5), i64(5)) // base ok, ver equal
	if !errors.Is(err, ErrSettingStaleVersion) {
		t.Fatalf("err = %v, want ErrSettingStaleVersion", err)
	}
	if v, _, _ := r.Get(ctx, metaKey); v != "60" {
		t.Fatalf("value = %q, want 60", v)
	}
}

func TestSetFromCloud_NoVer_UnconditionalAsToday_BumpsLocalVer(t *testing.T) {
	r, ctx := metaRepo(t), context.Background()
	_ = r.Set(ctx, metaKey, "30") // ver 2
	if err := r.SetFromCloud(ctx, metaKey, "90", nil, nil); err != nil {
		t.Fatal(err)
	}
	if v, _, _ := r.Get(ctx, metaKey); v != "90" {
		t.Fatalf("value = %q, want 90", v)
	}
	if m, _ := r.SettingMeta(ctx, metaKey); m.Ver != 3 || m.Origin != SettingOriginCloud {
		t.Fatalf("meta = %+v, want ver 3 origin cloud", m)
	}
}

func TestSetFromCloud_BaseVerEqualLocal_Applies(t *testing.T) {
	r, ctx := metaRepo(t), context.Background()
	_ = r.Set(ctx, metaKey, "30") // ver 2
	if err := r.SetFromCloud(ctx, metaKey, "50", i64(3), i64(2)); err != nil {
		t.Fatalf("base_ver == local ver must apply: %v", err)
	}
}
