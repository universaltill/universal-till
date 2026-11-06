package data

import (
	"context"
	"testing"
	"time"
)

func TestRevokedRelease_ExactVersionAndAllVersions(t *testing.T) {
	_, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()
	now := time.Now()
	if _, err := repo.ReplaceRevokedReleases(ctx, []RevokedRelease{
		{ManifestID: "com.x.exact", Version: "1.0.0", Reason: "bad"},
		{ManifestID: "com.x.all", Version: "", Reason: "worse"},
	}, now); err != nil {
		t.Fatal(err)
	}

	got, ok, err := repo.RevokedRelease(ctx, nil, "com.x.exact", "1.0.0")
	if err != nil || !ok || got.Reason != "bad" {
		t.Fatalf("exact v1: got=%+v ok=%v err=%v", got, ok, err)
	}
	if _, ok, _ := repo.RevokedRelease(ctx, nil, "com.x.exact", "2.0.0"); ok {
		t.Fatal("v2 must not match a v1 record")
	}
	for _, v := range []string{"1.0.0", "9.9.9"} {
		if _, ok, err := repo.RevokedRelease(ctx, nil, "com.x.all", v); err != nil || !ok {
			t.Fatalf("empty-version record must match %s: ok=%v err=%v", v, ok, err)
		}
	}
	if _, ok, _ := repo.RevokedRelease(ctx, nil, "com.x.other", "1.0.0"); ok {
		t.Fatal("other id must not match")
	}
}

func TestReplaceRevokedReleases_RemovesUnlisted(t *testing.T) {
	_, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()
	now := time.Now()
	if _, err := repo.ReplaceRevokedReleases(ctx, []RevokedRelease{
		{ManifestID: "com.x.a", Version: "1.0.0"},
		{ManifestID: "com.x.b", Version: "1.0.0"},
	}, now); err != nil {
		t.Fatal(err)
	}
	removed, err := repo.ReplaceRevokedReleases(ctx, []RevokedRelease{{ManifestID: "com.x.a", Version: "1.0.0", Reason: "updated"}}, now)
	if err != nil {
		t.Fatal(err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, ok, _ := repo.RevokedRelease(ctx, nil, "com.x.b", "1.0.0"); ok {
		t.Fatal("unlisted record must be lifted")
	}
	if got, ok, _ := repo.RevokedRelease(ctx, nil, "com.x.a", "1.0.0"); !ok || got.Reason != "updated" {
		t.Fatalf("listed record must be upserted: %+v ok=%v", got, ok)
	}
	if removed, err := repo.ReplaceRevokedReleases(ctx, nil, now); err != nil || removed != 1 {
		t.Fatalf("empty set clears all: removed=%d err=%v", removed, err)
	}
}

// An exact-version and an all-versions record may coexist; the exact one
// wins deterministically (ut-docs#5658 review N1).
func TestRevokedRelease_ExactRecordWinsOverAllVersions(t *testing.T) {
	_, repo := newPluginLifecycleTestDB(t)
	ctx := context.Background()
	if _, err := repo.ReplaceRevokedReleases(ctx, []RevokedRelease{
		{ManifestID: "com.x.both", Version: "", Reason: "all"},
		{ManifestID: "com.x.both", Version: "1.0.0", Reason: "exact"},
	}, time.Now()); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 5; i++ {
		got, ok, err := repo.RevokedRelease(ctx, nil, "com.x.both", "1.0.0")
		if err != nil || !ok || got.Reason != "exact" {
			t.Fatalf("got %+v ok=%v err=%v, want the exact record", got, ok, err)
		}
	}
	got, _, _ := repo.RevokedRelease(ctx, nil, "com.x.both", "2.0.0")
	if got.Reason != "all" {
		t.Fatalf("other version got %+v, want the all-versions record", got)
	}
}
