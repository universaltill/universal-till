package cloudsync

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

// ut-docs#3466 (ADR-0142 D1/D3): kiosk_unlock carries only {device_id}. It
// is device-targeted like rename_till and print_report — Tick skips one
// that is not this till's own (no apply, no result post) — and any-till,
// never main-till only. The directive id and its created_by subject reach
// the hook from the directive itself, for the till's audit row.

func TestDeviceTargetSkipReason(t *testing.T) {
	orig := ownDeviceID
	t.Cleanup(func() { ownDeviceID = orig })
	ownDeviceID = func() string { return "dev-self" }

	for _, typ := range []string{"rename_till", "print_report", "kiosk_unlock"} {
		d := func(dev any) directive {
			p := map[string]any{}
			if dev != nil {
				p["device_id"] = dev
			}
			return directive{ID: "x1", Type: typ, Payload: p}
		}
		for name, tc := range map[string]struct {
			d    directive
			want string
		}{
			"own device":         {d("dev-self"), ""},
			"own device, padded": {d(" dev-self "), ""},
			"other device":       {d("dev-other"), "addressed to another till"},
			"blank device":       {d(""), "addressed to another till"},
			"absent device":      {d(nil), "addressed to another till"},
			"non-string device":  {d(7.0), "addressed to another till"},
		} {
			if got := deviceTargetSkipReason(tc.d); got != tc.want {
				t.Errorf("%s/%s: %q, want %q", typ, name, got, tc.want)
			}
		}
	}
	// A type that names no device is never this check's business, whatever
	// its payload says.
	if got := deviceTargetSkipReason(directive{Type: "set_setting", Payload: map[string]any{"device_id": "dev-other"}}); got != "" {
		t.Fatalf("untargeted type: %q, want \"\"", got)
	}
	ownDeviceID = func() string { return "" }
	if got := deviceTargetSkipReason(directive{Type: "kiosk_unlock", Payload: map[string]any{"device_id": "dev-self"}}); got != "this till's own device id is not known yet" {
		t.Fatalf("own id unknown: %q", got)
	}
}

// ADR-0142 D1: a satellite holding its own device credential honours its
// own unlock.
func TestKioskUnlockIsNotMainTillOnly(t *testing.T) {
	if mainTillOnlyTypes["kiosk_unlock"] {
		t.Fatal("kiosk_unlock must not be main-till only")
	}
}

func TestApplyKioskUnlock(t *testing.T) {
	ctx := context.Background()
	d := directive{ID: "k1", Type: "kiosk_unlock", CreatedBy: "owner-sub", Payload: map[string]any{"device_id": "dev-1"}}

	if status, msg := apply(ctx, d, Hooks{}); status != "failed" || msg != "kiosk_unlock is not supported on this till" {
		t.Fatalf("nil hook: %s %q", status, msg)
	}

	var gotID, gotBy string
	hooks := Hooks{KioskUnlock: func(_ context.Context, id, by string) (string, error) {
		gotID, gotBy = id, by
		return "kiosk released", nil
	}}
	if status, msg := apply(ctx, d, hooks); status != "applied" || msg != "kiosk released" {
		t.Fatalf("applied: %s %q", status, msg)
	}
	if gotID != "k1" || gotBy != "owner-sub" {
		t.Fatalf("hook got id=%q created_by=%q", gotID, gotBy)
	}

	failing := Hooks{KioskUnlock: func(context.Context, string, string) (string, error) {
		return "", errors.New("not_self_order")
	}}
	if status, msg := apply(ctx, d, failing); status != "failed" || msg != "not_self_order" {
		t.Fatalf("failed: %s %q", status, msg)
	}
}

func TestTickKioskUnlockOnlyForOwnDevice(t *testing.T) {
	orig := ownDeviceID
	ownDeviceID = func() string { return "dev-self" }
	t.Cleanup(func() { ownDeviceID = orig })
	resetTargetSkipLog()
	t.Cleanup(resetTargetSkipLog)

	for _, role := range []struct {
		name    string
		primary string
	}{{"main till", ""}, {"satellite", "http://10.0.0.2:8080"}} {
		for _, tc := range []struct {
			name      string
			deviceID  any
			hookErr   error
			wantRuns  int
			wantPosts int
			wantState string
		}{
			{"own device", "dev-self", nil, 1, 1, "applied"},
			{"own device, refused", "dev-self", errors.New("not_self_order"), 1, 1, "failed"},
			{"other device", "dev-other", nil, 0, 0, ""},
			{"blank device", "", nil, 0, 0, ""},
			{"absent device", nil, nil, 0, 0, ""},
		} {
			t.Run(role.name+"/"+tc.name, func(t *testing.T) {
				payload := map[string]any{}
				if tc.deviceID != nil {
					payload["device_id"] = tc.deviceID
				}
				cloud := &fakeCloud{directives: []map[string]any{
					{"id": "k1", "type": "kiosk_unlock", "created_by": "owner-sub", "payload": payload},
				}}
				srv := httptest.NewServer(cloud.handler())
				defer srv.Close()
				db := testDB(t)
				if role.primary != "" {
					if _, err := db.Exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url', ?)`, role.primary); err != nil {
						t.Fatal(err)
					}
				}
				ran := 0
				var gotID, gotBy string
				hooks := Hooks{KioskUnlock: func(_ context.Context, id, by string) (string, error) {
					ran++
					gotID, gotBy = id, by
					if tc.hookErr != nil {
						return "", tc.hookErr
					}
					return "kiosk released", nil
				}}
				if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
					t.Fatalf("tick: %v", err)
				}
				if ran != tc.wantRuns || len(cloud.results) != tc.wantPosts {
					t.Fatalf("hook runs = %d, result posts = %+v; want %d runs, %d posts", ran, cloud.results, tc.wantRuns, tc.wantPosts)
				}
				if tc.wantRuns == 1 && (gotID != "k1" || gotBy != "owner-sub") {
					t.Fatalf("hook got id=%q created_by=%q", gotID, gotBy)
				}
				if tc.wantPosts == 1 && cloud.results[0]["status"] != tc.wantState {
					t.Fatalf("result = %+v, want %s", cloud.results[0], tc.wantState)
				}
			})
		}
	}
}
