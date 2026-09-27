package enroll

import (
	"context"
	"testing"
)

// ut-docs#3019: a main till registers under the till.name its owner typed;
// a replica only ever under its own sync.till_name (till.name there is the
// main till's, copied by the admin sync).
func TestDeviceNameMainTillUsesTillName(t *testing.T) {
	kv := newFakeKV()
	_ = kv.Set(context.Background(), keyTillName, "Front Counter")

	if got := DeviceName(context.Background(), kv); got != "Front Counter" {
		t.Fatalf("DeviceName = %q, want %q", got, "Front Counter")
	}
}

func TestDeviceNameMainTillFallsBackToSyncTillNameWhenTillNameBlank(t *testing.T) {
	kv := newFakeKV()
	_ = kv.Set(context.Background(), keySyncTillName, "Legacy Name")

	if got := DeviceName(context.Background(), kv); got != "Legacy Name" {
		t.Fatalf("DeviceName = %q, want the sync.till_name fallback %q", got, "Legacy Name")
	}
}

func TestDeviceNameMainTillTrimsWhitespace(t *testing.T) {
	kv := newFakeKV()
	_ = kv.Set(context.Background(), keyTillName, "  Front Counter  ")

	if got := DeviceName(context.Background(), kv); got != "Front Counter" {
		t.Fatalf("DeviceName = %q, want trimmed %q", got, "Front Counter")
	}
}

func TestDeviceNameReplicaUsesSyncTillNameOnly(t *testing.T) {
	kv := newFakeKV()
	_ = kv.Set(context.Background(), keySyncPrimaryURL, "https://main.example.lan")
	_ = kv.Set(context.Background(), keyTillName, "Main Till's Name")
	_ = kv.Set(context.Background(), keySyncTillName, "Back Office")

	if got := DeviceName(context.Background(), kv); got != "Back Office" {
		t.Fatalf("DeviceName = %q, want the replica's own sync.till_name %q", got, "Back Office")
	}
}

// The architect's key subtlety: a replica must NEVER fall back to till.name
// (that setting holds the MAIN till's name on a replica, per the admin sync
// comment), even when its own sync.till_name is blank.
func TestDeviceNameReplicaNeverFallsBackToTillName(t *testing.T) {
	kv := newFakeKV()
	_ = kv.Set(context.Background(), keySyncPrimaryURL, "https://main.example.lan")
	_ = kv.Set(context.Background(), keyTillName, "Main Till's Name")

	if got := DeviceName(context.Background(), kv); got != "" {
		t.Fatalf("DeviceName = %q, want empty (no fallback to till.name on a replica)", got)
	}
}

func TestDeviceNameReplicaTrimsWhitespace(t *testing.T) {
	kv := newFakeKV()
	_ = kv.Set(context.Background(), keySyncPrimaryURL, "https://main.example.lan")
	_ = kv.Set(context.Background(), keySyncTillName, "  Back Office  ")

	if got := DeviceName(context.Background(), kv); got != "Back Office" {
		t.Fatalf("DeviceName = %q, want trimmed %q", got, "Back Office")
	}
}
