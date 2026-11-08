package data

import (
	"context"
	"errors"
	"testing"
)

// ut-docs#5903: the pull tick's bookkeeping writes must not land after a
// committed promote (ClearReplicaIdentity), or they re-create the sync.* keys
// promote just deleted.

func settingPresent(t *testing.T, repo *SettingsRepo, key string) (string, bool) {
	t.Helper()
	v, ok, err := repo.Get(context.Background(), key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	return v, ok && v != ""
}

func TestSetManyIfReplica_RefusesAfterPromote_5903(t *testing.T) {
	ctx := context.Background()
	d := openReplicaDB(t, "replica.db")
	repo := NewSettingsRepo(d.DB)
	if err := repo.ClearReplicaIdentity(ctx); err != nil {
		t.Fatalf("promote: %v", err)
	}
	err := repo.SetManyIfReplica(ctx, map[string]string{"sync.till_role": "satellite", "sync.last_contact_at": "x"})
	if !errors.Is(err, ErrNotReplica) {
		t.Fatalf("err = %v, want ErrNotReplica", err)
	}
	for _, k := range []string{"sync.till_role", "sync.last_contact_at"} {
		if v, ok := settingPresent(t, repo, k); ok {
			t.Fatalf("%s written after promote: %q", k, v)
		}
	}
}

func TestSetManyIfReplica_RefusesBlankPrimaryURL_5903(t *testing.T) {
	ctx := context.Background()
	d := openReplicaDB(t, "replica.db")
	mustExec(t, d, `UPDATE settings SET value = '  ' WHERE key = 'sync.primary_url'`)
	repo := NewSettingsRepo(d.DB)
	err := repo.SetManyIfReplica(ctx, map[string]string{"sync.till_role": "satellite"})
	if !errors.Is(err, ErrNotReplica) {
		t.Fatalf("err = %v, want ErrNotReplica", err)
	}
	if v, ok := settingPresent(t, repo, "sync.till_role"); ok {
		t.Fatalf("sync.till_role written with blank primary_url: %q", v)
	}
}

func TestSetManyIfReplica_WritesOnReplica_5903(t *testing.T) {
	ctx := context.Background()
	d := openReplicaDB(t, "replica.db")
	repo := NewSettingsRepo(d.DB)
	if err := repo.SetManyIfReplica(ctx, map[string]string{"sync.till_role": "satellite", "sync.last_contact_at": "x"}); err != nil {
		t.Fatalf("set many on a replica: %v", err)
	}
	if v, _ := settingPresent(t, repo, "sync.till_role"); v != "satellite" {
		t.Fatalf("sync.till_role = %q", v)
	}
	if v, _ := settingPresent(t, repo, "sync.last_contact_at"); v != "x" {
		t.Fatalf("sync.last_contact_at = %q", v)
	}
}
