package enroll

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// ut-docs#5145: the cloud refuses to re-register a device that was in use in
// the last 72 h (409 device_live, data.usable_from). The till keeps selling,
// does not hammer, and tries again by itself once the time has passed.

func deviceLiveBody(usableFrom string) string {
	return fmt.Sprintf(`{"data":{"usable_from":%q},"error":{"code":"device_live","message":"this device was in use recently"}}`, usableFrom)
}

func TestIsDeviceLive(t *testing.T) {
	at := time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC) // fixed-date:ok far-future constant, no real-clock check reads it
	for _, c := range []struct {
		name     string
		err      error
		wantOK   bool
		wantTime time.Time
	}{
		{"nil", nil, false, time.Time{}},
		{"409 device_live", newRegisterHTTPError(409, deviceLiveBody(at.Format(time.RFC3339))), true, at},
		{"wrapped", fmt.Errorf("outer: %w", newRegisterHTTPError(409, deviceLiveBody(at.Format(time.RFC3339)))), true, at},
		{"unparseable usable_from is still device_live, zero time", newRegisterHTTPError(409, deviceLiveBody("soon")), true, time.Time{}},
		{"no data at all", newRegisterHTTPError(409, `{"error":{"code":"device_live"}}`), true, time.Time{}},
		{"other 409 code", newRegisterHTTPError(409, `{"error":{"code":"device_revoked"}}`), false, time.Time{}},
		{"403 device_live", &RegisterHTTPError{Status: 403, Code: "device_live"}, false, time.Time{}},
		{"plain error", errors.New("409 device_live"), false, time.Time{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, ok := IsDeviceLive(c.err)
			if ok != c.wantOK || !got.Equal(c.wantTime) {
				t.Fatalf("IsDeviceLive = (%v, %v), want (%v, %v)", got, ok, c.wantTime, c.wantOK)
			}
		})
	}
}

// countingMarketplace refuses every register with a 409 device_live carrying
// usableFrom and counts the calls.
func countingMarketplace(t *testing.T, usableFrom string, hits *atomic.Int32) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/api/v1/stores/register", func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		w.WriteHeader(http.StatusConflict)
		_, _ = w.Write([]byte(deviceLiveBody(usableFrom)))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestDeviceLiveBlocksTheLazyRetryUntilUsableFromButNotRegisterNow(t *testing.T) {
	resetState()
	var hits atomic.Int32
	usable := time.Now().UTC().Add(2 * time.Hour).Truncate(time.Second)
	srv := countingMarketplace(t, usable.Format(time.RFC3339), &hits)
	cfg := freshConfig(srv.URL)
	kv := newFakeKV()

	_, err := RegisterNow(context.Background(), cfg, kv)
	if _, ok := IsDeviceLive(err); !ok {
		t.Fatalf("RegisterNow err = %v, want device_live", err)
	}
	if got := RegisterBlockedUntil(context.Background(), kv); !got.Equal(usable) {
		t.Fatalf("blocked until %v, want %v", got, usable)
	}
	if hits.Load() != 1 {
		t.Fatalf("hits = %d", hits.Load())
	}

	// The lazy path (a plugin install, the wizard) does not call the cloud.
	for i := 0; i < 3; i++ {
		EnsureRegistered(context.Background(), cfg, kv)
	}
	if hits.Load() != 1 {
		t.Fatalf("lazy retries hit the cloud %d times while blocked", hits.Load()-1)
	}
	// A deliberate "Register now" always tries (staff may have lifted it).
	_, _ = RegisterNow(context.Background(), cfg, kv)
	if hits.Load() != 2 {
		t.Fatalf("Register now hits = %d, want 2", hits.Load())
	}

	// Past usable_from the lazy path retries by itself.
	if err := kv.Set(context.Background(), keyRegisterBlockedUntil, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if got := RegisterBlockedUntil(context.Background(), kv); !got.IsZero() {
		t.Fatalf("a past block still reports %v", got)
	}
	EnsureRegistered(context.Background(), cfg, kv)
	if hits.Load() != 3 {
		t.Fatalf("hits after usable_from = %d, want 3", hits.Load())
	}
}

func TestDeviceLiveWithoutUsableFromBlocksForAnHour(t *testing.T) {
	resetState()
	var hits atomic.Int32
	srv := countingMarketplace(t, "soon", &hits)
	kv := newFakeKV()
	_, _ = RegisterNow(context.Background(), freshConfig(srv.URL), kv)
	got := RegisterBlockedUntil(context.Background(), kv)
	if d := time.Until(got); d < 59*time.Minute || d > 61*time.Minute {
		t.Fatalf("blocked for %v, want about 1 h", d)
	}
}

func TestSuccessfulRegisterClearsTheBlock(t *testing.T) {
	resetState()
	srv, _ := testMarketplace(t, 0)
	kv := newFakeKV()
	if err := kv.Set(context.Background(), keyRegisterBlockedUntil, time.Now().UTC().Add(time.Hour).Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	cfg := freshConfig(srv.URL)
	initForTest(t, cfg, kv)
	if _, err := RegisterNow(context.Background(), cfg, kv); err != nil {
		t.Fatal(err)
	}
	if got := RegisterBlockedUntil(context.Background(), kv); !got.IsZero() || kv.get(keyRegisterBlockedUntil) != "" {
		t.Fatalf("block survived a successful register: %v %q", got, kv.get(keyRegisterBlockedUntil))
	}
}
