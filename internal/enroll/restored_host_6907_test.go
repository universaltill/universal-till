package enroll

import (
	"context"
	"errors"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
)

// ut-docs#6907: a data folder (secrets/ included) copied onto other hardware
// owes the #6294 number jump once. Hosts are simulated by swapping readHostID;
// the real machine id is never read.

func withHost(t *testing.T, src, raw string) {
	t.Helper()
	orig := readHostID
	readHostID = func() (string, string) { return src, raw }
	t.Cleanup(func() { readHostID = orig })
}

// bootOn runs the check as a boot on host (machine-id, raw).
func bootOn(t *testing.T, kv Settings, cfg *config.Config, raw string, rec *oweRecorder) {
	t.Helper()
	withHost(t, "machine-id", raw)
	if cfg == nil {
		cfg = &config.Config{}
	}
	CheckRestoredInstall(context.Background(), cfg, kv, nil, rec.fn)
}

// settledKV is an enrolled main till that has booted once on H1: marker and
// DB install id agree and the host tag is recorded.
func settledKV(t *testing.T) *orderedKV {
	t.Helper()
	restoreDir(t)
	kv := enrolledKV(t)
	bootOn(t, kv, nil, "H1", &oweRecorder{}) // adopts install id + tag
	return kv
}

func TestHostMove_MainOwesJumpOnce(t *testing.T) {
	kv := settledKV(t)
	installA := kv.get(keyInstallID)
	tagH1 := kv.get(keyHostTag)
	if installA == "" || tagH1 == "" {
		t.Fatalf("first boot must record install id %q and tag %q", installA, tagH1)
	}
	rec := &oweRecorder{}
	bootOn(t, kv, nil, "H2", rec)
	if len(rec.tokens) != 1 || rec.tokens[0] != "copied-host:"+installA {
		t.Fatalf("owe calls = %v", rec.tokens)
	}
	if kv.get(keyHostTag) == tagH1 || kv.get(keyHostTag) == "" {
		t.Fatalf("tag not re-recorded: %q", kv.get(keyHostTag))
	}
	if id := readMarker(t); id == installA || kv.get(keyInstallID) != id {
		t.Fatalf("install id not re-marked: marker %q row %q", id, kv.get(keyInstallID))
	}
	rec2 := &oweRecorder{}
	bootOn(t, kv, nil, "H2", rec2)
	if len(rec2.tokens) != 0 {
		t.Fatalf("second boot on H2 owes %v", rec2.tokens)
	}
}

func TestHostMove_OriginalOwesNothing_RebootThrice(t *testing.T) {
	kv := settledKV(t)
	before := len(kv.writes)
	rec := &oweRecorder{}
	for i := 0; i < 3; i++ {
		bootOn(t, kv, nil, "H1", rec)
	}
	if len(rec.tokens) != 0 {
		t.Fatalf("owes %v", rec.tokens)
	}
	if len(kv.writes) != before {
		t.Fatalf("reboots wrote settings: %v", kv.writes[before:])
	}
}

func TestHostMove_UpgradeAdoptsThenDetects(t *testing.T) {
	restoreDir(t)
	kv := enrolledKV(t)
	// An upgraded till: install id and marker agree, no host tag yet.
	bootOn(t, kv, nil, "H0", &oweRecorder{})
	_ = kv.fakeKV.Set(context.Background(), keyHostTag, "")
	rec := &oweRecorder{}
	bootOn(t, kv, nil, "H1", rec)
	if len(rec.tokens) != 0 || kv.get(keyHostTag) == "" {
		t.Fatalf("upgrade boot: owes %v tag %q", rec.tokens, kv.get(keyHostTag))
	}
	bootOn(t, kv, nil, "H2", rec)
	if len(rec.tokens) != 1 {
		t.Fatalf("after the move owes %v", rec.tokens)
	}
}

func TestHostMove_UnknownHostNoop(t *testing.T) {
	kv := settledKV(t)
	tag := kv.get(keyHostTag)
	rec := &oweRecorder{}
	withHost(t, "", "")
	CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil, rec.fn)
	if len(rec.tokens) != 0 || kv.get(keyHostTag) != tag {
		t.Fatalf("unknown host: owes %v tag %q", rec.tokens, kv.get(keyHostTag))
	}
}

func TestHostMove_OffSwitchSkipsEverything(t *testing.T) {
	kv := settledKV(t)
	tag := kv.get(keyHostTag)
	rec := &oweRecorder{}
	bootOn(t, kv, &config.Config{HostID: "OFF"}, "H2", rec)
	if len(rec.tokens) != 0 || kv.get(keyHostTag) != tag {
		t.Fatalf("off: owes %v tag %q", rec.tokens, kv.get(keyHostTag))
	}
}

func TestHostMove_PinIgnoresRealHost(t *testing.T) {
	restoreDir(t)
	kv := enrolledKV(t)
	cfg := &config.Config{HostID: "pin-1"}
	bootOn(t, kv, cfg, "X1", &oweRecorder{}) // adopt, pinned
	rec := &oweRecorder{}
	bootOn(t, kv, cfg, "X2", rec) // the machine id changed, the pin did not
	if len(rec.tokens) != 0 {
		t.Fatalf("pinned host owes %v", rec.tokens)
	}
	bootOn(t, kv, &config.Config{HostID: "pin-2"}, "X2", rec)
	if len(rec.tokens) != 1 {
		t.Fatalf("a different pin is a different host, owes %v", rec.tokens)
	}
}

func TestHostMove_PinTransitionAdopts(t *testing.T) {
	kv := settledKV(t) // machine-id source
	rec := &oweRecorder{}
	bootOn(t, kv, &config.Config{HostID: "pin-1"}, "H1", rec)
	if len(rec.tokens) != 0 || !strings.HasPrefix(kv.get(keyHostTag), "pinned:") {
		t.Fatalf("into pin: owes %v tag %q", rec.tokens, kv.get(keyHostTag))
	}
	bootOn(t, kv, nil, "H9", rec) // pin removed: out of pinned
	if len(rec.tokens) != 0 || !strings.HasPrefix(kv.get(keyHostTag), "machine-id:") {
		t.Fatalf("out of pin: owes %v tag %q", rec.tokens, kv.get(keyHostTag))
	}
}

func TestHostMove_SourceChangeCountsAsMoved(t *testing.T) {
	kv := settledKV(t)
	withHost(t, "dt-serial", "H1")
	rec := &oweRecorder{}
	CheckRestoredInstall(context.Background(), &config.Config{}, kv, nil, rec.fn)
	if len(rec.tokens) != 1 {
		t.Fatalf("owes %v", rec.tokens)
	}
}

func TestHostMove_OweFailsRetriesSameToken(t *testing.T) {
	kv := settledKV(t)
	installA, tag := kv.get(keyInstallID), kv.get(keyHostTag)
	logs := captureLog(t)
	bad := &oweRecorder{fail: errors.New("disk full")}
	bootOn(t, kv, nil, "H2", bad)
	if kv.get(keyHostTag) != tag || kv.get(keyInstallID) != installA {
		t.Fatal("tag or install id changed although the jump could not be owed")
	}
	if !strings.Contains(logs.String(), "disk full") {
		t.Fatalf("failure not logged: %s", logs.String())
	}
	rec := &oweRecorder{}
	bootOn(t, kv, nil, "H2", rec)
	if len(rec.tokens) != 1 || rec.tokens[0] != bad.tokens[0] {
		t.Fatalf("retry token %v, first %v", rec.tokens, bad.tokens)
	}
}

type failTagKV struct{ *orderedKV }

func (f failTagKV) Set(ctx context.Context, key, value string) error {
	if key == keyHostTag && value != "" && f.get(keyHostTag) != "" {
		return errors.New("tag write failed")
	}
	return f.orderedKV.Set(ctx, key, value)
}

func TestHostMove_TagWriteFailsNoRemark(t *testing.T) {
	kv := settledKV(t)
	installA := kv.get(keyInstallID)
	rec := &oweRecorder{}
	bootOn(t, failTagKV{kv}, nil, "H2", rec)
	if kv.get(keyInstallID) != installA || readMarker(t) != installA {
		t.Fatal("re-marked although the tag could not be recorded")
	}
}

func TestHostMove_ReplicaGetsProvisionalPrefixNoJump(t *testing.T) {
	restoreDir(t)
	kv := replicaKV(t)
	bootOn(t, kv, nil, "H1", &oweRecorder{})
	_ = kv.fakeKV.Set(context.Background(), keySyncReceiptPrefix, "T2-")
	_ = kv.fakeKV.Set(context.Background(), keySyncBearer, lanBearer)
	rec := &oweRecorder{}
	bootOn(t, kv, nil, "H2", rec)
	if len(rec.tokens) != 0 {
		t.Fatalf("replica owes %v", rec.tokens)
	}
	if !provisionalPrefix.MatchString(kv.get(keySyncReceiptPrefix)) {
		t.Fatalf("prefix %q", kv.get(keySyncReceiptPrefix))
	}
	if kv.get(keySyncBearer) != "" || kv.get(keySyncTillID) != "" {
		t.Fatal("bearer/till id not cleared")
	}
	if kv.get(keyHostTag) == "" {
		t.Fatal("tag not recorded")
	}
}

func TestHostMove_MainKeepsCloudIdentity(t *testing.T) {
	kv := settledKV(t)
	keys := []string{keyDeviceID, keyDeviceRegistered, keyToken, keyStoreID, keyMerchantID, keyPairedAt, keyRestoredAt}
	before := map[string]string{}
	for _, k := range keys {
		before[k] = kv.get(k)
	}
	bootOn(t, kv, nil, "H2", &oweRecorder{})
	for _, k := range keys {
		if kv.get(k) != before[k] {
			t.Errorf("%s changed: %q -> %q", k, before[k], kv.get(k))
		}
	}
	if kv.get(keyRestoredAt) != "" {
		t.Error("restored_at must stay unset")
	}
}

func TestHostMove_TagIsHMACOnly(t *testing.T) {
	kv := settledKV(t)
	if !regexp.MustCompile(`^machine-id:[0-9a-f]{64}$`).MatchString(kv.get(keyHostTag)) {
		t.Fatalf("tag %q", kv.get(keyHostTag))
	}
	logs := captureLog(t)
	bootOn(t, kv, nil, "SECRET-RAW-ID", &oweRecorder{})
	for k, v := range kv.fakeKV.m {
		if strings.Contains(v, "SECRET-RAW-ID") || strings.Contains(v, "H1") && k == keyHostTag {
			t.Errorf("raw id in %s=%q", k, v)
		}
	}
	if strings.Contains(logs.String(), "SECRET-RAW-ID") || strings.Contains(logs.String(), kv.get(keyHostTag)) {
		t.Fatalf("raw id or tag logged: %s", logs.String())
	}
}

func TestHostMove_CopyBackOwesAgain(t *testing.T) {
	kv := settledKV(t)
	rec := &oweRecorder{}
	bootOn(t, kv, nil, "H2", rec)
	bootOn(t, kv, nil, "H1", rec)
	if len(rec.tokens) != 2 || rec.tokens[0] == rec.tokens[1] {
		t.Fatalf("tokens %v", rec.tokens)
	}
}
