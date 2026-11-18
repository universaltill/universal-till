package enroll

import (
	"context"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/db"
)

// ut-docs#5058 (design §3.3 T2, ADR-0117 amendment 2026-10-26): a till the
// owner replaced learns it from the cloud's 401 device_revoked
// {reason: replaced}. The state is persisted so it survives a restart (the
// till must stop advertising itself as main till before its first check-in),
// and it is part of this till's cloud identity: a fresh pairing clears it,
// and it never travels to another till.

func TestMarkReplacedPersistsAndReads(t *testing.T) {
	ctx := context.Background()
	kv := newFakeKV()
	if _, ok := ReplacedState(ctx, kv); ok {
		t.Fatal("a fresh till reads as replaced")
	}
	MarkReplaced(ctx, kv, "Counter 2", "2026-10-26T10:00:00Z")
	st, ok := ReplacedState(ctx, kv)
	if !ok || st.By != "Counter 2" || st.At != "2026-10-26T10:00:00Z" {
		t.Fatalf("state = %+v ok=%v", st, ok)
	}
	// A later answer with the successor's name fills an empty one in; an
	// empty name never erases a known one.
	MarkReplaced(ctx, kv, "", "2026-10-26T10:00:00Z")
	if st, _ := ReplacedState(ctx, kv); st.By != "Counter 2" {
		t.Fatalf("an empty replaced_by erased the name: %+v", st)
	}
}

func TestMarkReplacedClampsCloudText(t *testing.T) {
	ctx := context.Background()
	kv := newFakeKV()
	MarkReplaced(ctx, kv, strings.Repeat("n", 500), strings.Repeat("t", 500))
	st, _ := ReplacedState(ctx, kv)
	if len([]rune(st.By)) > 80 || len(st.At) > 40 {
		t.Fatalf("cloud text not clamped: by=%d at=%d", len(st.By), len(st.At))
	}
}

func TestReplacedKeysAreTillCloudIdentity(t *testing.T) {
	for _, k := range []string{keyReplacedBy, keyReplacedAt, keyReplaced} {
		covered := false
		for _, p := range db.TillCloudIdentityPrefixes {
			if strings.HasPrefix(k, p) {
				covered = true
			}
		}
		if !covered {
			t.Errorf("%s is not under db.TillCloudIdentityPrefixes: it would travel in a join snapshot or a restored backup", k)
		}
	}
}

func TestPairingClearsReplacedState(t *testing.T) {
	ctx := context.Background()
	kv := newFakeKV()
	MarkReplaced(ctx, kv, "Counter 2", "2026-10-26T10:00:00Z")
	resetIdentityForPair(ctx, kv, false, "till-new")
	if _, ok := ReplacedState(ctx, kv); ok {
		t.Fatal("a fresh pairing kept the replaced state: the new identity would show 'Replaced by'")
	}
}

func TestStoreLeftHookRunsOnlyAfterPairingToADifferentShop(t *testing.T) {
	ctx := context.Background()
	var left []string
	OnStoreLeft = func(old string) { left = append(left, old) }
	t.Cleanup(func() { OnStoreLeft = nil })

	// Resetting the identity (before the new pairing) never fires the hook,
	// and reports the shop being left; a replica leaves nothing.
	kv := newFakeKV()
	kv.m[keyStoreID] = "st_old"
	if got := resetIdentityForPair(ctx, kv, false, "till-new"); got != "st_old" {
		t.Fatalf("left shop = %q, want st_old", got)
	}
	if len(left) != 0 {
		t.Fatalf("hook ran before the new pairing succeeded: %v", left)
	}
	rep := newFakeKV()
	rep.m[keyStoreID] = "st_old"
	if got := resetIdentityForPair(ctx, rep, true, "till-new"); got != "" {
		t.Fatalf("a replica reported leaving %q", got)
	}

	storeLeft("st_old", "st_old") // re-pair to the SAME shop
	storeLeft("", "st_new")       // no previous shop
	if len(left) != 0 {
		t.Fatalf("hook ran for the same shop / no old shop: %v", left)
	}
	storeLeft("st_old", "st_new")
	if len(left) != 1 || left[0] != "st_old" {
		t.Fatalf("hook calls = %v, want [st_old]", left)
	}
}
