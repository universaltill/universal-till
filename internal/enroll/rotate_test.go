package enroll

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/logging"
)

// ut-docs#2769, ADR-0116 D4: the cloud rotates a till off the store's
// shared (legacy) token by answering a /v1/stores/sync with this device's
// own credential in data.device_id + data.device_token. These tests pin the
// till side: kept only for this till's own device id, persisted where the
// token already lives (marketplace.token, per-till and never replicated),
// used from the very next request without a restart, never logged, and
// ignored (with one warning per process) when the environment pins the
// token.

const (
	rotOwnDevice = "till-own"
	rotLegacy    = "legacy-shared-token-0001"
	rotNew       = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
)

// countingKV counts writes, so "the same token again is a no-op" is
// provable.
type countingKV struct {
	*fakeKV
	mu   sync.Mutex
	sets map[string]int
}

func newCountingKV() *countingKV { return &countingKV{fakeKV: newFakeKV(), sets: map[string]int{}} }

func (c *countingKV) Set(ctx context.Context, key, value string) error {
	c.mu.Lock()
	c.sets[key]++
	c.mu.Unlock()
	return c.fakeKV.Set(ctx, key, value)
}

func (c *countingKV) setsOf(key string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sets[key]
}

// initEnrolledTill boots an already-enrolled main till holding the legacy
// token. A pinned signing key and a registered device keep Init from
// starting any background loop.
func initEnrolledTill(t *testing.T, cfg *config.Config) *countingKV {
	t.Helper()
	resetState()
	kv := newCountingKV()
	for k, v := range map[string]string{
		keyDeviceID:         rotOwnDevice,
		keyStoreID:          "store-1",
		keyMerchantID:       "store-1",
		keyToken:            rotLegacy,
		keyPublicKey:        strings.Repeat("cd", 32),
		keyDeviceRegistered: rotOwnDevice,
	} {
		_ = kv.fakeKV.Set(context.Background(), k, v)
	}
	Init(context.Background(), cfg, kv, &sync.WaitGroup{})
	t.Cleanup(resetState)
	return kv
}

func captureLog(t *testing.T) *syncBuffer {
	t.Helper()
	buf := &syncBuffer{}
	restore := logging.CaptureForTest(buf)
	t.Cleanup(restore)
	return buf
}

type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (s *syncBuffer) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.Write(p)
}

func (s *syncBuffer) String() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.b.String()
}

func TestRotatedCredential_OwnDeviceIsPersistedAndUsedWithoutRestart(t *testing.T) {
	logs := captureLog(t)
	cfg := freshConfig("http://cloud.invalid")
	kv := initEnrolledTill(t, cfg)
	if got := Effective(cfg).Marketplace.MerchantToken; got != rotLegacy {
		t.Fatalf("precondition: effective token = %q, want the legacy one", got)
	}

	ApplyRotatedCredential(context.Background(), kv, rotOwnDevice, rotNew)

	if got := kv.get(keyToken); got != rotNew {
		t.Fatalf("persisted %s = %q, want the rotated credential", keyToken, got)
	}
	if got := Effective(cfg).Marketplace.MerchantToken; got != rotNew {
		t.Fatalf("effective token = %q, want the rotated credential without a restart", got)
	}
	// The startup copy in cfg (replicaLoop / run hold one) must not win
	// over the rotated credential either.
	if _, tok := currentStoreAuth(cfg.Marketplace); tok != rotNew {
		t.Fatalf("currentStoreAuth token = %q, want the rotated credential", tok)
	}
	if !CurrentStatus().Registered {
		t.Fatal("till no longer registered after rotation")
	}
	if strings.Contains(logs.String(), rotNew) {
		t.Fatalf("the rotated credential reached the log:\n%s", logs.String())
	}

	// A restart loads it from marketplace.token like any stored token.
	cfg2 := freshConfig("http://cloud.invalid")
	resetState()
	Init(context.Background(), cfg2, kv, &sync.WaitGroup{})
	if got := Effective(cfg2).Marketplace.MerchantToken; got != rotNew {
		t.Fatalf("after restart effective token = %q, want the rotated credential", got)
	}
}

func TestRotatedCredential_SameTokenAgainIsNoOp(t *testing.T) {
	cfg := freshConfig("http://cloud.invalid")
	kv := initEnrolledTill(t, cfg)
	ApplyRotatedCredential(context.Background(), kv, rotOwnDevice, rotNew)
	ApplyRotatedCredential(context.Background(), kv, rotOwnDevice, rotNew)
	if n := kv.setsOf(keyToken); n != 1 {
		t.Fatalf("%s written %d times, want exactly 1", keyToken, n)
	}
}

func TestRotatedCredential_OtherOrMissingDeviceIsIgnored(t *testing.T) {
	for _, tc := range []struct{ name, deviceID string }{
		{"another till's device", "till-other"},
		{"no device id", ""},
		{"own id with padding is not own id", " " + rotOwnDevice},
	} {
		t.Run(tc.name, func(t *testing.T) {
			logs := captureLog(t)
			cfg := freshConfig("http://cloud.invalid")
			kv := initEnrolledTill(t, cfg)

			ApplyRotatedCredential(context.Background(), kv, tc.deviceID, rotNew)

			if got := kv.get(keyToken); got != rotLegacy {
				t.Fatalf("persisted token = %q, want the legacy one untouched", got)
			}
			if got := Effective(cfg).Marketplace.MerchantToken; got != rotLegacy {
				t.Fatalf("effective token = %q, want the legacy one", got)
			}
			out := logs.String()
			if strings.Count(out, "[WARN]") != 1 {
				t.Fatalf("want exactly one warning, got:\n%s", out)
			}
			if strings.Contains(out, rotNew) {
				t.Fatalf("the refused credential reached the log:\n%s", out)
			}
		})
	}
}

func TestRotatedCredential_NoFieldsLeavesTokenUntouched(t *testing.T) {
	logs := captureLog(t)
	cfg := freshConfig("http://cloud.invalid")
	kv := initEnrolledTill(t, cfg)

	ApplyRotatedCredential(context.Background(), kv, "", "")
	ApplyRotatedCredential(context.Background(), kv, rotOwnDevice, "")

	if n := kv.setsOf(keyToken); n != 0 {
		t.Fatalf("%s written %d times, want 0", keyToken, n)
	}
	if got := Effective(cfg).Marketplace.MerchantToken; got != rotLegacy {
		t.Fatalf("effective token = %q, want the legacy one", got)
	}
	if strings.Contains(logs.String(), "[WARN]") {
		t.Fatalf("a sync with no rotation must not warn:\n%s", logs.String())
	}
}

func TestRotatedCredential_MalformedTokenIsRefused(t *testing.T) {
	for _, tc := range []struct{ name, token string }{
		{"whitespace", "abc def0123456789abcdef"},
		{"newline", "abcdef0123456789\nabcdef"},
		{"too short", "abc"},
		{"too long", strings.Repeat("a", 1025)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := freshConfig("http://cloud.invalid")
			kv := initEnrolledTill(t, cfg)
			ApplyRotatedCredential(context.Background(), kv, rotOwnDevice, tc.token)
			if got := kv.get(keyToken); got != rotLegacy {
				t.Fatalf("persisted token = %q, want the legacy one untouched", got)
			}
		})
	}
}

func TestRotatedCredential_EnvPinnedTokenIsKeptAndWarnsOnce(t *testing.T) {
	logs := captureLog(t)
	cfg := freshConfig("http://cloud.invalid")
	cfg.Marketplace.MerchantToken = "env-pinned-token-0001" // UT_MARKETPLACE_MERCHANT_TOKEN
	kv := initEnrolledTill(t, cfg)

	ApplyRotatedCredential(context.Background(), kv, rotOwnDevice, rotNew)
	ApplyRotatedCredential(context.Background(), kv, rotOwnDevice, rotNew)

	if n := kv.setsOf(keyToken); n != 0 {
		t.Fatalf("%s written %d times while the env pins the token, want 0", keyToken, n)
	}
	if got := Effective(cfg).Marketplace.MerchantToken; got != "env-pinned-token-0001" {
		t.Fatalf("effective token = %q, want the env-pinned one", got)
	}
	out := logs.String()
	if n := strings.Count(out, "UT_MARKETPLACE_MERCHANT_TOKEN"); n != 1 {
		t.Fatalf("want exactly one env-pin warning, got %d:\n%s", n, out)
	}
	if strings.Contains(out, rotNew) {
		t.Fatalf("the rotated credential reached the log:\n%s", out)
	}
}

// failOnceKV fails the first write of keyToken, like a busy SQLite file.
type failOnceKV struct {
	*countingKV
	failed atomic.Bool
}

func (f *failOnceKV) Set(ctx context.Context, key, value string) error {
	if key == keyToken && f.failed.CompareAndSwap(false, true) {
		return errors.New("database is locked")
	}
	return f.countingKV.Set(ctx, key, value)
}

// The cloud sends a rotated credential once. A failed save keeps it in use
// and is retried from the check-in tick, not only on the next sync POST
// (ut-docs#2769 review S2).
func TestRotatedCredential_FailedSaveIsRetriedFromTheCheckinTick(t *testing.T) {
	cfg := freshConfig("http://cloud.invalid")
	kv := &failOnceKV{countingKV: initEnrolledTill(t, cfg)}

	ApplyRotatedCredential(context.Background(), kv, rotOwnDevice, rotNew)
	if got := kv.get(keyToken); got != rotLegacy {
		t.Fatalf("precondition: the first save should have failed, stored %q", got)
	}
	if got := Effective(cfg).Marketplace.MerchantToken; got != rotNew {
		t.Fatalf("in-use token = %q, want the rotated one despite the failed save", got)
	}

	RetryUnsavedCredential(context.Background(), kv)
	if got := kv.get(keyToken); got != rotNew {
		t.Fatalf("after the check-in retry stored %q, want the rotated credential", got)
	}
}
