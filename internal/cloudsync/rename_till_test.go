package cloudsync

import (
	"context"
	"net/http/httptest"
	"testing"
)

// ut-docs#3272: rename_till carries {device_id, name}. The dispatch needs
// the hook and a name; the device targeting lives in Tick.

func TestApplyRenameTill(t *testing.T) {
	ctx := context.Background()
	d := func(p map[string]any) directive { return directive{ID: "d1", Type: "rename_till", Payload: p} }

	if status, msg := apply(ctx, d(map[string]any{"device_id": "dev-1", "name": "Bar"}), Hooks{}); status != "failed" || msg != "rename_till is not supported on this till" {
		t.Fatalf("nil hook: %q %q", status, msg)
	}

	var got []string
	hooks := Hooks{RenameTill: func(_ context.Context, name string) (string, error) {
		got = append(got, name)
		return "till renamed", nil
	}}
	for _, p := range []map[string]any{
		{"device_id": "dev-1"},
		{"device_id": "dev-1", "name": "   "},
		{"device_id": "dev-1", "name": 7.0},
	} {
		if status, msg := apply(ctx, d(p), hooks); status != "failed" || msg != "missing name" {
			t.Errorf("%v: %q %q, want failed/missing name", p, status, msg)
		}
	}
	if len(got) != 0 {
		t.Fatalf("hook ran for an invalid payload: %v", got)
	}

	status, msg := apply(ctx, d(map[string]any{"device_id": "dev-1", "name": "  Bar Till "}), hooks)
	if status != "applied" || msg != "till renamed" || len(got) != 1 || got[0] != "Bar Till" {
		t.Fatalf("apply = %q %q, hook got %v", status, msg, got)
	}
}

// rename_till applies on a satellite too: it names the till it targets,
// whichever role that till has.
func TestRenameTillIsNotMainTillOnly(t *testing.T) {
	if mainTillOnlyTypes["rename_till"] {
		t.Fatal("rename_till must not be main-till only")
	}
}

// Defence in depth: the cloud serves rename_till only to its target's own
// sync, but a directive whose device_id is blank or another till's is
// skipped outright (no apply, no result post), so it stays pending for its
// real target. On both the main till and a satellite.
func TestTickRenameTillOnlyForOwnDevice(t *testing.T) {
	orig := ownDeviceID
	ownDeviceID = func() string { return "dev-self" }
	t.Cleanup(func() { ownDeviceID = orig })
	resetRenameSkipLog()
	t.Cleanup(resetRenameSkipLog)

	for _, role := range []struct {
		name    string
		primary string
	}{{"main till", ""}, {"satellite", "http://10.0.0.2:8080"}} {
		for _, tc := range []struct {
			name      string
			deviceID  any
			wantRuns  int
			wantPosts int
		}{
			{"own device", "dev-self", 1, 1},
			{"own device, padded", " dev-self ", 1, 1},
			{"other device", "dev-other", 0, 0},
			{"blank device", "", 0, 0},
			{"absent device", nil, 0, 0},
		} {
			t.Run(role.name+"/"+tc.name, func(t *testing.T) {
				payload := map[string]any{"name": "Bar"}
				if tc.deviceID != nil {
					payload["device_id"] = tc.deviceID
				}
				cloud := &fakeCloud{directives: []map[string]any{
					{"id": "r1", "type": "rename_till", "payload": payload},
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
				hooks := Hooks{RenameTill: func(_ context.Context, name string) (string, error) {
					ran++
					return "till renamed", nil
				}}
				if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
					t.Fatalf("tick: %v", err)
				}
				if ran != tc.wantRuns || len(cloud.results) != tc.wantPosts {
					t.Fatalf("hook runs = %d, result posts = %+v; want %d runs, %d posts", ran, cloud.results, tc.wantRuns, tc.wantPosts)
				}
			})
		}
	}
}

// A till that does not know its own device id yet skips every rename_till
// rather than applying one meant for some other till.
func TestTickRenameTillSkippedWhenOwnIDUnknown(t *testing.T) {
	orig := ownDeviceID
	ownDeviceID = func() string { return "" }
	t.Cleanup(func() { ownDeviceID = orig })

	cloud := &fakeCloud{directives: []map[string]any{
		{"id": "r1", "type": "rename_till", "payload": map[string]any{"device_id": "", "name": "Bar"}},
	}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	ran := 0
	hooks := Hooks{RenameTill: func(context.Context, string) (string, error) { ran++; return "", nil }}
	if err := Tick(context.Background(), testCfg(srv.URL), testDB(t), hooks); err != nil {
		t.Fatalf("tick: %v", err)
	}
	if ran != 0 || len(cloud.results) != 0 {
		t.Fatalf("runs %d, posts %+v; want none", ran, cloud.results)
	}
}

func TestRenameSkipLoggedOncePerDirective(t *testing.T) {
	resetRenameSkipLog()
	t.Cleanup(resetRenameSkipLog)
	if !firstRenameSkip("r1") || firstRenameSkip("r1") {
		t.Fatal("r1 must be reported on its first skip only")
	}
	if !firstRenameSkip("r2") {
		t.Fatal("a different directive must be reported")
	}
}
