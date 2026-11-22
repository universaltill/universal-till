package enroll

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
)

// ut-docs#6294: a restored MAIN till owes the receipt/Z number jump; the debt
// is recorded before the re-mark, because detection fires on one boot only.

type oweRecorder struct {
	tokens []string
	fail   error
	onCall func()
}

func (o *oweRecorder) fn(token string) error {
	o.tokens = append(o.tokens, token)
	if o.onCall != nil {
		o.onCall()
	}
	return o.fail
}

func TestRestoreCheck_MainTillOwesJumpBeforeRemark(t *testing.T) {
	restoreDir(t)
	kv := enrolledKV(t)
	_ = kv.fakeKV.Set(context.Background(), keyInstallID, "install-A")
	rec := &oweRecorder{}
	rec.onCall = func() {
		if _, err := os.Stat(markerPath()); err == nil {
			t.Error("install marker already written when the jump was owed")
		}
		if got := kv.get(keyInstallID); got != "install-A" {
			t.Errorf("DB install id already %q when the jump was owed", got)
		}
	}
	if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil, rec.fn) {
		t.Fatal("want reset")
	}
	if len(rec.tokens) != 1 || rec.tokens[0] != "restored-install:install-A" {
		t.Fatalf("owe calls = %v", rec.tokens)
	}
	if id := readMarker(t); id == "" || kv.get(keyInstallID) != id {
		t.Fatalf("marker %q / row %q, want re-marked after owing", id, kv.get(keyInstallID))
	}
}

// A till that never enrolled (no token, registered or paired rows) still owes
// the jump: numbers are local, independent of the cloud identity.
func TestRestoreCheck_NeverEnrolledMainTillOwesJump(t *testing.T) {
	restoreDir(t)
	kv := newOrderedKV()
	_ = kv.fakeKV.Set(context.Background(), keyInstallID, "install-A")
	rec := &oweRecorder{}
	CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil, rec.fn)
	if len(rec.tokens) != 1 || rec.tokens[0] != "restored-install:install-A" {
		t.Fatalf("owe calls = %v", rec.tokens)
	}
}

func TestRestoreCheck_EnvPinnedMainTillOwesJump(t *testing.T) {
	for name, cfg := range map[string]*config.Config{
		"device id": {Marketplace: config.MarketplaceConfig{DeviceID: "pinned"}},
		"client id": {Marketplace: config.MarketplaceConfig{ClientID: "pinned"}},
		"token":     {Marketplace: config.MarketplaceConfig{MerchantToken: "pinned"}},
	} {
		t.Run(name, func(t *testing.T) {
			restoreDir(t)
			kv := enrolledKV(t)
			_ = kv.fakeKV.Set(context.Background(), keyInstallID, "install-A")
			rec := &oweRecorder{}
			if CheckRestoredInstall(context.Background(), cfg, kv, nil, rec.fn) {
				t.Fatal("pinned identity is not reset")
			}
			if len(rec.tokens) != 1 || rec.tokens[0] != "restored-install:install-A" {
				t.Fatalf("owe calls = %v", rec.tokens)
			}
		})
	}
}

func TestRestoreCheck_OweFailureSkipsRemark(t *testing.T) {
	restoreDir(t)
	logs := captureLog(t)
	kv := enrolledKV(t)
	_ = kv.fakeKV.Set(context.Background(), keyInstallID, "install-A")
	rec := &oweRecorder{fail: errors.New("disk full")}
	if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil, rec.fn) {
		t.Fatal("the identity reset still happened and is reported")
	}
	if _, err := os.Stat(markerPath()); err == nil {
		t.Fatal("install marker written although the jump could not be owed")
	}
	if got := kv.get(keyInstallID); got != "install-A" {
		t.Fatalf("install id = %q, want unchanged so the next boot detects again", got)
	}
	if kv.get(keyToken) != "" {
		t.Fatal("identity must still be reset")
	}
	if !strings.Contains(logs.String(), "disk full") {
		t.Fatalf("failure not logged: %s", logs.String())
	}
	// Next boot: owes the same token, then re-marks.
	rec2 := &oweRecorder{}
	CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil, rec2.fn)
	if len(rec2.tokens) != 1 || rec2.tokens[0] != "restored-install:install-A" {
		t.Fatalf("retry owe calls = %v", rec2.tokens)
	}
	if id := readMarker(t); kv.get(keyInstallID) != id || id == "install-A" {
		t.Fatalf("marker %q / row %q, want re-marked on the retry", id, kv.get(keyInstallID))
	}
}

func TestRestoreCheck_OweFailureSkipsRemark_EnvPinned(t *testing.T) {
	restoreDir(t)
	kv := enrolledKV(t)
	_ = kv.fakeKV.Set(context.Background(), keyInstallID, "install-A")
	rec := &oweRecorder{fail: errors.New("boom")}
	cfg := &config.Config{Marketplace: config.MarketplaceConfig{DeviceID: "pinned"}}
	CheckRestoredInstall(context.Background(), cfg, kv, nil, rec.fn)
	if got := kv.get(keyInstallID); got != "install-A" {
		t.Fatalf("install id = %q, want unchanged", got)
	}
}

func TestRestoreCheck_ReplicaOwesNoJump(t *testing.T) {
	restoreDir(t)
	kv := replicaKV(t)
	rec := &oweRecorder{}
	if !CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil, rec.fn) {
		t.Fatal("want reset")
	}
	if len(rec.tokens) != 0 {
		t.Fatalf("a restored replica owes no jump, got %v", rec.tokens)
	}
	if !strings.HasPrefix(kv.get(keySyncReceiptPrefix), "R") {
		t.Fatalf("receipt prefix = %q, want provisional R<hex>-", kv.get(keySyncReceiptPrefix))
	}
}

func TestRestoreCheck_NormalBootAndAdoptOweNoJump(t *testing.T) {
	restoreDir(t)
	rec := &oweRecorder{}
	// Fresh install / upgrade: adopt.
	CheckRestoredInstall(context.Background(), &config.Config{}, enrolledKV(t), nil, rec.fn)
	// Normal restart.
	kv := enrolledKV(t)
	writeMarker(t, "install-A")
	_ = kv.fakeKV.Set(context.Background(), keyInstallID, "install-A")
	CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil, rec.fn)
	if len(rec.tokens) != 0 {
		t.Fatalf("owe calls = %v, want none", rec.tokens)
	}
}
