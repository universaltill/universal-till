package cloudsync

// ut-docs#6975 root cause 4: an install_plugin result carries the install's
// message KEY (data.message_key) so my. can show a translated reason per
// till, and a failed install's message never carries the raw error text
// (paths, URLs) the hook's error may hold.

import (
	"context"
	"errors"
	"net/http/httptest"
	"testing"
)

func installDirective(id string) map[string]any {
	return map[string]any{"id": id, "type": "install_plugin", "payload": map[string]any{"listing_id": "lst-1"}}
}

func tickInstall(t *testing.T, name string, hooks Hooks) map[string]any {
	t.Helper()
	cloud := &guestCloud{batches: [][]map[string]any{{installDirective("d1")}}}
	srv := httptest.NewServer(cloud.handler())
	defer srv.Close()
	db := openMigratedDB(t, name).DB
	if err := Tick(context.Background(), testCfg(srv.URL), db, hooks); err != nil {
		t.Fatal(err)
	}
	res := cloud.snapshot()
	if len(res) != 1 {
		t.Fatalf("want one result post, got %v", res)
	}
	return res[0]
}

func TestTick_InstallPluginFailureReportsMessageKey(t *testing.T) {
	hooks := Hooks{
		InstallPlugin: func(context.Context, string) (string, error) {
			return "", &ReasonError{Key: "plugins.install.error.invalid_package", Message: "The plugin package failed verification.", Cause: errors.New("open /Users/x/plugins/tmp/a.part: no such file")}
		},
	}
	r := tickInstall(t, "r6975_fail.db", hooks)
	if r["status"] != "failed" {
		t.Fatalf("status = %v", r["status"])
	}
	if r["message"] != "The plugin package failed verification." {
		t.Fatalf("message must be the safe text, got %v", r["message"])
	}
	d, _ := r["data"].(map[string]any)
	if d["message_key"] != "plugins.install.error.invalid_package" {
		t.Fatalf("data = %v", r["data"])
	}
}

func TestTick_InstallPluginSuccessHasNoMessageKey(t *testing.T) {
	hooks := Hooks{InstallPlugin: func(context.Context, string) (string, error) { return "installed", nil }}
	r := tickInstall(t, "r6975_ok.db", hooks)
	if r["status"] != "applied" {
		t.Fatalf("status = %v", r["status"])
	}
	if _, has := r["data"]; has {
		t.Fatalf("an applied install carries no data block: %v", r)
	}
}

// The first result post fails and the cloud re-delivers the directive: the
// re-posted stored result carries the same key, and the hook runs once.
func TestTick_InstallPluginKeySurvivesRepost(t *testing.T) {
	calls := 0
	hooks := Hooks{InstallPlugin: func(context.Context, string) (string, error) {
		calls++
		return "", &ReasonError{Key: "plugins.install.error.unreachable", Message: "Can't reach the plugin marketplace.", Cause: errors.New("dial tcp 10.0.0.5:443: refused")}
	}}
	c := redeliverTwice(t, "r6975_repost.db", installDirective("d1"), hooks)
	if calls != 1 {
		t.Fatalf("InstallPlugin ran %d times, want 1", calls)
	}
	r := c.snapshot()[1]
	d, _ := r["data"].(map[string]any)
	if d["message_key"] != "plugins.install.error.unreachable" || r["message"] != "Can't reach the plugin marketplace." {
		t.Fatalf("re-posted %v", r)
	}
}
