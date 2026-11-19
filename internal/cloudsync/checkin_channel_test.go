package cloudsync

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/enroll"
)

// checkin-notice-v2 lists "release channel" among the facts the till sends.
// The channel is the build-time install channel (ADR-0194), set by
// buildSyncRequest itself: neither the minimal allow-list nor a DeviceExtra
// value can stand in for it.

func deviceChannelOf(t *testing.T, body []byte) (string, bool) {
	t.Helper()
	var req struct {
		Devices []map[string]any `json:"devices"`
	}
	if err := json.Unmarshal(body, &req); err != nil || len(req.Devices) != 1 {
		t.Fatalf("body: %v %s", err, body)
	}
	v, ok := req.Devices[0]["channel"].(string)
	return v, ok
}

func TestMinimalCheckinCarriesTheBuildChannel(t *testing.T) {
	for _, ch := range []buildinfo.InstallChannel{buildinfo.ChannelPlay, buildinfo.ChannelDirect} {
		t.Run(string(ch), func(t *testing.T) {
			defer buildinfo.OverrideChannelForTest(ch)()
			useRealSyncGate(t)
			resetTillAuth(t)
			pinOperatorNow(t, startupDay)
			pinVersion(t, "0.30.20")
			srv, got := captureServer(t, 402)
			db := testDB(t)
			ctx := context.Background()
			if err := data.NewSettingsRepo(db).SetMany(ctx, map[string]string{
				"setup.completed":        "true",
				enroll.KeyNoticeShownAt:  time.Now().UTC().Format(time.RFC3339),
				enroll.KeyNoticeVersion:  enroll.NoticeVersion,
				enroll.KeyAutoRegistered: "true",
			}); err != nil {
				t.Fatal(err)
			}
			armStartup()
			_, _ = tick(ctx, testCfg(srv.URL), db, fatHooks())
			bodies := got()
			if len(bodies) != 1 {
				t.Fatalf("POSTs = %d, want 1", len(bodies))
			}
			// fatHooks also offers channel "stable": it must not win.
			if v, ok := deviceChannelOf(t, bodies[0]); !ok || v != string(ch) {
				t.Errorf("minimal report channel = %q (present %v), want %q", v, ok, ch)
			}
		})
	}
}

func TestNormalCheckinCarriesTheBuildChannel(t *testing.T) {
	defer buildinfo.OverrideChannelForTest(buildinfo.ChannelSideload)()
	srv, got := captureServer(t, 200)
	db := testDB(t)
	_, _ = tick(context.Background(), testCfg(srv.URL), db, fatHooks())
	var body []byte
	for _, b := range got() {
		if len(b) > 0 {
			body = b // the sync POST; a conditional GET has no body
		}
	}
	if body == nil {
		t.Fatal("a normal till made no check-in POST")
	}
	if v, ok := deviceChannelOf(t, body); !ok || v != string(buildinfo.ChannelSideload) {
		t.Errorf("full report channel = %q (present %v), want %q", v, ok, buildinfo.ChannelSideload)
	}
}
