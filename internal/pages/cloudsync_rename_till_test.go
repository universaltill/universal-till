package pages

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#3272: the rename_till hook. Main till → till.name; an additional
// till (sync.primary_url set) → its own sync.till_name. Either way
// enroll.DeviceName, which the heartbeat reports, returns the new name.

func renameTillAudits(t *testing.T, dp *common.Deps) []map[string]any {
	t.Helper()
	rows, err := dp.Db.QueryContext(t.Context(),
		`SELECT actor_id, entity_type, entity_id, data_json FROM audit_log WHERE action = 'till_name_changed' ORDER BY created_at`)
	if err != nil {
		t.Fatalf("query audit: %v", err)
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var actor, typ, id, js string
		if err := rows.Scan(&actor, &typ, &id, &js); err != nil {
			t.Fatalf("scan audit: %v", err)
		}
		row := map[string]any{}
		if err := json.Unmarshal([]byte(js), &row); err != nil {
			t.Fatalf("audit payload %q: %v", js, err)
		}
		row["_actor"], row["_entity_type"], row["_entity_id"] = actor, typ, id
		out = append(out, row)
	}
	return out
}

func settingValue(t *testing.T, dp *common.Deps, key string) string {
	t.Helper()
	v, _, err := dp.Settings.Get(t.Context(), key)
	if err != nil {
		t.Fatalf("get %s: %v", key, err)
	}
	return v
}

func TestCloudRenameTill_MainTillWritesTillName(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if err := dp.Settings.Set(ctx, "till.name", "Front Counter"); err != nil {
		t.Fatal(err)
	}

	msg, err := cloudRenameTill(ctx, dp, "Bar Till")
	if err != nil {
		t.Fatalf("cloudRenameTill: %v", err)
	}
	if msg != "till renamed" {
		t.Fatalf("msg = %q", msg)
	}
	if got := settingValue(t, dp, "till.name"); got != "Bar Till" {
		t.Fatalf("till.name = %q", got)
	}
	if got := settingValue(t, dp, "sync.till_name"); got != "" {
		t.Fatalf("sync.till_name written on a main till: %q", got)
	}
	if got := enroll.DeviceName(ctx, dp.Settings); got != "Bar Till" {
		t.Fatalf("DeviceName = %q", got)
	}
	audits := renameTillAudits(t, dp)
	if len(audits) != 1 {
		t.Fatalf("audit rows = %+v, want 1", audits)
	}
	a := audits[0]
	if a["name"] != "Bar Till" || a["via"] != "cloud" || a["key"] != "till.name" || a["_actor"] != "system" || a["_entity_type"] != "settings" || a["_entity_id"] != "till.name" {
		t.Fatalf("audit row = %+v", a)
	}
}

func TestCloudRenameTill_AdditionalTillWritesSyncTillName(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	for k, v := range map[string]string{
		"sync.primary_url": "http://primary.example",
		"till.name":        "Main Till's Name",
		"sync.till_name":   "Back Office",
	} {
		if err := dp.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := cloudRenameTill(ctx, dp, "Terrace"); err != nil {
		t.Fatalf("cloudRenameTill: %v", err)
	}
	if got := settingValue(t, dp, "sync.till_name"); got != "Terrace" {
		t.Fatalf("sync.till_name = %q", got)
	}
	if got := settingValue(t, dp, "till.name"); got != "Main Till's Name" {
		t.Fatalf("till.name (the main till's, shop-wide) changed on an additional till: %q", got)
	}
	if got := enroll.DeviceName(ctx, dp.Settings); got != "Terrace" {
		t.Fatalf("DeviceName = %q", got)
	}
	audits := renameTillAudits(t, dp)
	if len(audits) != 1 || audits[0]["key"] != "sync.till_name" || audits[0]["via"] != "cloud" || audits[0]["name"] != "Terrace" {
		t.Fatalf("audit rows = %+v", audits)
	}
}

func TestCloudRenameTill_RefusesInvalidNamesWritingNothing(t *testing.T) {
	for _, tc := range []struct {
		name, value string
	}{
		{"empty", ""},
		{"blank", "   \t "},
		{"too long", strings.Repeat("é", maxTillNameRunes+1)},
		{"newline", "Bar\nTill"},
		{"nul", "Bar\x00Till"},
		{"escape", "Bar\x1b[2JTill"},
		{"DEL", "Bar\x7fTill"},
		{"C1 control", "Bar\u0085Till"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dp := newCloudSyncTestDeps(t)
			ctx := t.Context()
			if err := dp.Settings.Set(ctx, "till.name", "Front Counter"); err != nil {
				t.Fatal(err)
			}
			if _, err := cloudRenameTill(ctx, dp, tc.value); err == nil {
				t.Fatalf("%q accepted", tc.value)
			}
			if got := settingValue(t, dp, "till.name"); got != "Front Counter" {
				t.Fatalf("till.name = %q after a refused rename", got)
			}
			if got := settingValue(t, dp, "sync.till_name"); got != "" {
				t.Fatalf("sync.till_name = %q after a refused rename", got)
			}
			if a := renameTillAudits(t, dp); len(a) != 0 {
				t.Fatalf("audit rows after a refused rename: %+v", a)
			}
		})
	}
}

// Exactly the limit is fine, and is stored whole (never truncated).
func TestCloudRenameTill_AcceptsExactlyMaxRunes(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	name := strings.Repeat("é", maxTillNameRunes)
	if _, err := cloudRenameTill(t.Context(), dp, "  "+name+"  "); err != nil {
		t.Fatalf("cloudRenameTill: %v", err)
	}
	if got := settingValue(t, dp, "till.name"); got != name {
		t.Fatalf("till.name = %q, want the full %d-rune name", got, maxTillNameRunes)
	}
}

func TestCloudRenameTill_UnchangedNameWritesNothing(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	ctx := t.Context()
	if err := dp.Settings.Set(ctx, "till.name", "Front Counter"); err != nil {
		t.Fatal(err)
	}
	msg, err := cloudRenameTill(ctx, dp, " Front Counter ")
	if err != nil || msg != "name unchanged" {
		t.Fatalf("cloudRenameTill = %q, %v; want \"name unchanged\"", msg, err)
	}
	if a := renameTillAudits(t, dp); len(a) != 0 {
		t.Fatalf("audit rows for an unchanged name: %+v", a)
	}
}

// The hook is wired: buildCloudHooks sets RenameTill.
func TestBuildCloudHooks_WiresRenameTill(t *testing.T) {
	dp := newCloudSyncTestDeps(t)
	hooks := buildCloudHooks(dp, func(context.Context) {})
	if hooks.RenameTill == nil {
		t.Fatal("RenameTill hook not wired")
	}
	if _, err := hooks.RenameTill(t.Context(), "Wired"); err != nil {
		t.Fatalf("RenameTill: %v", err)
	}
	if got := settingValue(t, dp, "till.name"); got != "Wired" {
		t.Fatalf("till.name = %q", got)
	}
}

func TestValidateTillName(t *testing.T) {
	if got, err := validateTillName("  Bar  "); err != nil || got != "Bar" {
		t.Fatalf("validateTillName = %q, %v", got, err)
	}
	if _, err := validateTillName("Tab\tInside"); err == nil {
		t.Fatal("a tab inside the name must be refused")
	}
}
