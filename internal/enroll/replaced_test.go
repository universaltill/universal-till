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
