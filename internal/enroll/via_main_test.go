package enroll

import (
	"context"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
)

// ut-docs#2753: a replica its main till registered has no store token of its
// own, so Registered stays false; ViaMainTill carries the vouched state.

func setCur(id identity) {
	mu.Lock()
	cur = id
	mu.Unlock()
}

// newReplicaKV is a fake settings store for a till joined to a main till.
func newReplicaKV() *fakeKV {
	kv := newFakeKV()
	_ = kv.Set(context.Background(), keySyncPrimaryURL, "http://main.local:8080")
	return kv
}

func TestViaMainTillTrueAfterVouchOnTokenlessReplica(t *testing.T) {
	resetState()
	kv := newReplicaKV()
	setCur(identity{DeviceID: "till-own"})
	if CurrentStatus().ViaMainTill {
		t.Fatal("ViaMainTill before any vouch")
	}
	if err := applyVouch(context.Background(), config.MarketplaceConfig{}, kv, Vouch{DeviceID: "till-own"}); err != nil {
		t.Fatal(err)
	}
	s := CurrentStatus()
	if s.Registered || !s.ViaMainTill {
		t.Fatalf("status = %+v, want Registered=false ViaMainTill=true", s)
	}
}

func TestViaMainTillFalseWhenVouchNamesAnotherDevice(t *testing.T) {
	resetState()
	kv := newReplicaKV()
	setCur(identity{DeviceID: "till-own"})
	_ = applyVouch(context.Background(), config.MarketplaceConfig{}, kv, Vouch{DeviceID: "till-other"})
	if CurrentStatus().ViaMainTill {
		t.Fatal("ViaMainTill set by a vouch for another device")
	}
}

func TestViaMainTillFalseWhenTillHasOwnToken(t *testing.T) {
	resetState()
	kv := newReplicaKV()
	setCur(identity{DeviceID: "till-own", StoreID: "s", Token: "tok"})
	if err := applyVouch(context.Background(), config.MarketplaceConfig{}, kv, Vouch{DeviceID: "till-own"}); err != nil {
		t.Fatal(err)
	}
	s := CurrentStatus()
	if !s.Registered || s.ViaMainTill {
		t.Fatalf("status = %+v, want Registered=true ViaMainTill=false", s)
	}
}

func TestViaMainTillFalseWhenDeviceChanges(t *testing.T) {
	resetState()
	kv := newReplicaKV()
	setCur(identity{DeviceID: "till-own"})
	_ = applyVouch(context.Background(), config.MarketplaceConfig{}, kv, Vouch{DeviceID: "till-own"})
	setCur(identity{DeviceID: "till-new"})
	if CurrentStatus().ViaMainTill {
		t.Fatal("stale vouch applied to a different device id")
	}
}

func TestInitReplicaWithPersistedVouchSetsViaMainTill(t *testing.T) {
	resetState()
	kv := newFakeKV()
	_ = kv.Set(context.Background(), "sync.primary_url", "http://127.0.0.1:1")
	_ = kv.Set(context.Background(), keyDeviceID, "till-own")
	_ = kv.Set(context.Background(), keyDeviceRegistered, "till-own")
	initForTest(t, &config.Config{}, kv)
	s := CurrentStatus()
	if s.Registered || !s.ViaMainTill {
		t.Fatalf("status = %+v, want Registered=false ViaMainTill=true", s)
	}
}

func TestInitNonReplicaWithMarkerIsNotViaMainTill(t *testing.T) {
	resetState()
	kv := newFakeKV()
	_ = kv.Set(context.Background(), keyDeviceID, "till-own")
	_ = kv.Set(context.Background(), keyDeviceRegistered, "till-own")
	initForTest(t, &config.Config{}, kv)
	if CurrentStatus().ViaMainTill {
		t.Fatal("main till reported as registered via main till")
	}
}

func TestInitReplicaCopiedIdentityIsNotViaMainTill(t *testing.T) {
	resetState()
	kv := newFakeKV()
	seedCopiedReplica(kv, "http://127.0.0.1:1")
	delete(kv.m, keyToken)
	initForTest(t, &config.Config{}, kv)
	if CurrentStatus().ViaMainTill {
		t.Fatal("copied identity counted as vouched")
	}
}

func TestForgetReplicaVouchClears(t *testing.T) {
	resetState()
	kv := newReplicaKV()
	setCur(identity{DeviceID: "till-own"})
	_ = applyVouch(context.Background(), config.MarketplaceConfig{}, kv, Vouch{DeviceID: "till-own"})
	ForgetReplicaVouch()
	if CurrentStatus().ViaMainTill {
		t.Fatal("ViaMainTill after ForgetReplicaVouch")
	}
}

// A vouch answer that lands after the till was promoted (sync.primary_url
// already cleared, ForgetReplicaVouch already run) must not restore the
// via-main state.
func TestLateVouchAfterPromoteIsNotViaMainTill(t *testing.T) {
	resetState()
	kv := newReplicaKV()
	setCur(identity{DeviceID: "till-own"})
	_ = kv.Set(context.Background(), keySyncPrimaryURL, "")
	ForgetReplicaVouch()
	_ = applyVouch(context.Background(), config.MarketplaceConfig{}, kv, Vouch{DeviceID: "till-own"})
	if CurrentStatus().ViaMainTill {
		t.Fatal("late vouch after promote restored ViaMainTill")
	}
}
