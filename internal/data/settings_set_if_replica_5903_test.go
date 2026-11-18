package data

import (
	"context"
	"errors"
	"testing"
)

func TestSetIfReplica_RefusesWithoutPrimaryURL_5903(t *testing.T) {
	d := openMigratedDB(t, "setifreplica-no.db")
	err := NewSettingsRepo(d.DB).SetIfReplica(context.Background(), map[string]string{"sync.till_role": "satellite", "sync.last_pull_ok_at": "x"})
	if !errors.Is(err, ErrNotReplica) {
		t.Fatalf("err = %v, want ErrNotReplica", err)
	}
	var n int
	_ = d.QueryRow(`SELECT COUNT(*) FROM settings WHERE key IN ('sync.till_role','sync.last_pull_ok_at')`).Scan(&n)
	if n != 0 {
		t.Fatalf("%d keys written by a refused SetIfReplica", n)
	}
}

func TestSetIfReplica_WritesAllKeysOnReplica_5903(t *testing.T) {
	d := openReplicaDB(t, "setifreplica-yes.db")
	repo := NewSettingsRepo(d.DB)
	if err := repo.SetIfReplica(context.Background(), map[string]string{"sync.till_role": "satellite", "sync.last_pull_ok_at": "x"}); err != nil {
		t.Fatalf("SetIfReplica: %v", err)
	}
	for k, want := range map[string]string{"sync.till_role": "satellite", "sync.last_pull_ok_at": "x"} {
		if got, _, _ := repo.Get(context.Background(), k); got != want {
			t.Fatalf("%s = %q, want %q", k, got, want)
		}
	}
	if ok, err := repo.IsReplica(context.Background()); err != nil || !ok {
		t.Fatalf("IsReplica = %v, %v; want true", ok, err)
	}
}
