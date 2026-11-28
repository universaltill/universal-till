package pages

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/tills"
)

// ghostShop seeds the owner's four-row case (ut-docs#7098): the Mac's own
// register (self), phone-till2 (live, till row present), "phone till" (till
// row gone, replaced by phone-till2's device) and "iphone-till" (till row gone).
func ghostShop(t *testing.T) (*http.ServeMux, *common.Deps, auth.User) {
	t.Helper()
	mux, d := newRegistersTestMux(t)
	old := time.Now().Add(-90 * 24 * time.Hour).UTC().Format(time.RFC3339)
	for _, s := range []string{
		`INSERT INTO registers (id, name, is_active) VALUES ('reg-default','mac-till',1),('r-live','phone-till2',1),('r-g1','phone till',1),('r-g2','iphone-till',1)`,
		`INSERT INTO settings (key, value) VALUES ('sync.till_register_id','reg-default')`,
		`INSERT INTO tills (id, name, bearer_hash, device_id) VALUES ('tB','phone-till2','h','dev-B')`,
		`INSERT INTO register_devices (register_id, device_id, till_id, source, superseded_by) VALUES
		   ('r-live','dev-B','tB','enrol',''),('r-g1','dev-A','tA','enrol','dev-B'),('r-g2','dev-C','tC','enrol','')`,
		`INSERT INTO sales (id, receipt_no, subtotal, total, register_id, sync_status, created_at) VALUES
		   ('s1','R1',0,0,'r-g1','synced','` + old + `'),('s2','R2',0,0,'r-g2','synced','` + old + `')`,
	} {
		if _, err := d.Db.Exec(s); err != nil {
			t.Fatalf("seed %.50s: %v", s, err)
		}
	}
	return mux, d, auth.User{ID: "m1", Role: "manager", DisplayName: "Manager"}
}

func getRegisters(t *testing.T, mux *http.ServeMux, m auth.User) string {
	t.Helper()
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, auth.WithUser(httptest.NewRequest(http.MethodGet, "/registers", nil), m))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /registers = %d", rec.Code)
	}
	return rec.Body.String()
}

// rowOf returns the table row that holds name.
func regRowOf(t *testing.T, body, name string) string {
	t.Helper()
	i := strings.Index(body, `<span class="record-row-name">`+name)
	if i < 0 {
		i = strings.Index(body, `<span class="record-row-name"><span class="muted">`+name)
	}
	if i < 0 {
		t.Fatalf("no row for %q", name)
	}
	end := strings.Index(body[i:], "</tr>")
	return body[i : i+end]
}

// Self never gets a Retire button; the ghosts get the highlighted one; the
// live one the plain one; all words come from en.json.
func TestRegistersPageShowsRetireOnlyForRetirable(t *testing.T) {
	mux, _, m := ghostShop(t)
	body := getRegisters(t, mux, m)
	if row := regRowOf(t, body, "mac-till"); strings.Contains(row, "Retire") || !strings.Contains(row, "This till") {
		t.Fatalf("self row must say This till and offer no Retire: %s", row)
	}
	for _, name := range []string{"phone till", "iphone-till"} {
		row := regRowOf(t, body, name)
		if !strings.Contains(row, `aria-label="Retire register `+name+`"`) || strings.Contains(row, "btn secondary") {
			t.Fatalf("%s: want the highlighted Retire button: %s", name, row)
		}
	}
	if row := regRowOf(t, body, "phone till"); !strings.Contains(row, "Replaced by a newer device") {
		t.Fatalf("phone till status line: %s", row)
	}
	if row := regRowOf(t, body, "iphone-till"); !strings.Contains(row, "Old entry: this till is no longer in your account") {
		t.Fatalf("iphone-till status line: %s", row)
	}
	live := regRowOf(t, body, "phone-till2")
	if !strings.Contains(live, "Live") || !strings.Contains(live, "btn secondary") {
		t.Fatalf("live row: want Live and the plain Retire button: %s", live)
	}
	// the confirm names the register and its verb
	if !strings.Contains(regRowOf(t, body, "iphone-till"), `data-confirm-ok="Retire"`) {
		t.Fatal("Retire opener must name its verb (data-confirm-ok)")
	}
}

// With a single active register no row offers Retire.
func TestRegistersPageNoRetireOnLastActive(t *testing.T) {
	mux, d, m := ghostShop(t)
	if _, err := d.Db.Exec(`UPDATE registers SET is_active = 0 WHERE id <> 'reg-default'`); err != nil {
		t.Fatal(err)
	}
	body := getRegisters(t, mux, m)
	if strings.Contains(body, `aria-label="Retire register`) {
		t.Fatal("a Retire button is offered while only one register is active")
	}
	// retired rows offer Activate, with a reason line
	if row := regRowOf(t, body, "phone till"); !strings.Contains(row, "Activate") {
		t.Fatalf("retired row must offer Activate: %s", row)
	}
}

// AC9 till half: the four-row case is 2 active + 2 retire-safe candidates;
// after Retire on both: 2 active + 2 retired, sales untouched.
func TestFourRowCase(t *testing.T) {
	mux, d, m := ghostShop(t)
	var active int
	_ = d.Db.QueryRow(`SELECT COUNT(*) FROM registers WHERE is_active = 1`).Scan(&active)
	if active != 4 {
		t.Fatalf("seed active = %d", active)
	}
	for _, id := range []string{"r-g1", "r-g2"} {
		rec := postForm(mux, "/api/registers/"+id+"/active", url.Values{"active": {"0"}, "reason": {"manual"}}, &m)
		if rec.Code != http.StatusSeeOther || rec.Header().Get("Location") != "/registers" {
			t.Fatalf("retire %s: %d %q", id, rec.Code, rec.Header().Get("Location"))
		}
	}
	_ = d.Db.QueryRow(`SELECT COUNT(*) FROM registers WHERE is_active = 1`).Scan(&active)
	var retired, sales int
	_ = d.Db.QueryRow(`SELECT COUNT(*) FROM register_devices WHERE retired_reason = 'manual'`).Scan(&retired)
	_ = d.Db.QueryRow(`SELECT COUNT(*) FROM sales`).Scan(&sales)
	if active != 2 || retired != 2 || sales != 2 {
		t.Fatalf("active=%d retired=%d sales=%d, want 2 2 2", active, retired, sales)
	}
	body := getRegisters(t, mux, m)
	if row := regRowOf(t, body, "iphone-till"); !strings.Contains(row, "Retired Retired by a manager:") {
		t.Fatalf("retired row reason line: %s", row)
	}
}

// Test 20: the HTTP path uses the repo's guards.
func TestRetirePostUsesGuards(t *testing.T) {
	mux, d, m := ghostShop(t)
	row := url.Values{"active": {"0"}, "reason": {"manual"}}
	if rec := postForm(mux, "/api/registers/reg-default/active", row, &m); rec.Header().Get("Location") != "/registers?err=registers.error.retire_self" {
		t.Fatalf("self: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if _, err := d.Db.Exec(`INSERT INTO shifts (id, register_id, cashier_id, opened_at) VALUES ('open','r-g1','u1',datetime('now'))`); err != nil {
		// no users row: seed one
		_, _ = d.Db.Exec(`INSERT INTO users (id, username, display_name, role) VALUES ('u1','c1','C','cashier')`)
		if _, err := d.Db.Exec(`INSERT INTO shifts (id, register_id, cashier_id, opened_at) VALUES ('open','r-g1','u1',datetime('now'))`); err != nil {
			t.Fatal(err)
		}
	}
	if rec := postForm(mux, "/api/registers/r-g1/active", row, &m); rec.Header().Get("Location") != "/registers?err=registers.error.retire_open_shift" {
		t.Fatalf("open shift: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if _, err := d.Db.Exec(`INSERT INTO sales (id, receipt_no, subtotal, total, register_id, sync_status) VALUES ('q','Rq',0,0,'r-g2','queued')`); err != nil {
		t.Fatal(err)
	}
	if rec := postForm(mux, "/api/registers/r-g2/active", row, &m); rec.Header().Get("Location") != "/registers?err=registers.error.retire_unsent" {
		t.Fatalf("unsent: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	var n int
	_ = d.Db.QueryRow(`SELECT COUNT(*) FROM registers WHERE is_active = 1`).Scan(&n)
	if n != 4 {
		t.Fatalf("a refused retire changed state: active=%d", n)
	}
	// a replica refuses before anything else
	if _, err := d.Db.Exec(`INSERT INTO settings (key, value) VALUES ('sync.primary_url','http://main.invalid')`); err != nil {
		t.Fatal(err)
	}
	if rec := postForm(mux, "/api/registers/r-g1/active", row, &m); rec.Header().Get("Location") != "/registers?err=registers.error.replica_use_primary" {
		t.Fatalf("replica: %d %q", rec.Code, rec.Header().Get("Location"))
	}
	if body := getRegisters(t, mux, m); strings.Contains(body, `aria-label="Retire register`) || strings.Contains(body, "/registers/auto-retire") {
		t.Fatal("a replica must see no Retire button and no auto-retire control")
	}
}

// AC10: a null location is shown truthfully as the default location.
func TestRegisterNullLocationRendersMainDefault(t *testing.T) {
	mux, _, m := ghostShop(t)
	if row := regRowOf(t, getRegisters(t, mux, m), "phone-till2"); !strings.Contains(row, "Main location (default)") {
		t.Fatalf("a register with no location must read Main location (default): %s", row)
	}
}

func oldEntryRows(ids ...string) []tills.Row {
	var out []tills.Row
	for _, id := range ids {
		out = append(out, tills.Row{ID: id, State: tills.StateOldEntry})
	}
	return out
}

func activeIDs(t *testing.T, d *common.Deps) map[string]bool {
	t.Helper()
	rows, err := d.Db.Query(`SELECT id FROM registers WHERE is_active = 1`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[string]bool{}
	for rows.Next() {
		var id string
		_ = rows.Scan(&id)
		out[id] = true
	}
	return out
}

// Default OFF: an old-entry ghost stays active, whatever the account says.
func TestAutoRetireDefaultOff(t *testing.T) {
	_, d, _ := ghostShop(t)
	if n := maybeAutoRetireRegisters(t.Context(), d, oldEntryRows("tA", "tC"), true); n != 0 {
		t.Fatalf("retired %d with the flag absent, want 0", n)
	}
	if got := len(activeIDs(t, d)); got != 4 {
		t.Fatalf("active = %d, want 4", got)
	}
}

// Flag on, 30 days: only a known-device ghost quiet for 31 days goes; a recent
// sale, an unknown device, self, the live one, an unreadable account and a
// second call within 24 h all keep everything.
func TestAutoRetireOn30Days(t *testing.T) {
	_, d, _ := ghostShop(t)
	ctx := t.Context()
	set := func(k, v string) {
		t.Helper()
		if err := d.Settings.Set(ctx, k, v); err != nil {
			t.Fatal(err)
		}
	}
	set("registers.auto_retire_enabled", "1")
	// r-g1: last sale 31 days ago; r-g2: last sale 29 days ago
	for id, days := range map[string]int{"s1": 31, "s2": 29} {
		if _, err := d.Db.Exec(`UPDATE sales SET created_at = ? WHERE id = ?`, time.Now().Add(-time.Duration(days)*24*time.Hour).UTC().Format(time.RFC3339), id); err != nil {
			t.Fatal(err)
		}
	}
	// an old register with an unknown device id (never auto-retired)
	if _, err := d.Db.Exec(`INSERT INTO registers (id, name, is_active) VALUES ('r-unk','legacy',1)`); err != nil {
		t.Fatal(err)
	}
	if _, err := d.Db.Exec(`INSERT INTO register_devices (register_id, device_id, till_id) VALUES ('r-unk','','tU')`); err != nil {
		t.Fatal(err)
	}
	// account unreadable: nothing, and the throttle stamp is NOT consumed
	if n := maybeAutoRetireRegisters(ctx, d, nil, false); n != 0 {
		t.Fatalf("retired %d while the account list was unknown", n)
	}
	if _, ok, _ := d.Settings.Get(ctx, "registers.auto_retire_last_run"); ok {
		t.Fatal("an unreadable account list consumed the 24 h throttle")
	}
	if n := maybeAutoRetireRegisters(ctx, d, oldEntryRows("tA", "tC", "tU"), true); n != 1 {
		t.Fatalf("retired %d, want exactly 1 (r-g1)", n)
	}
	got := activeIDs(t, d)
	for id, want := range map[string]bool{"r-g1": false, "r-g2": true, "reg-default": true, "r-live": true, "r-unk": true} {
		if got[id] != want {
			t.Fatalf("%s active = %v, want %v (active: %v)", id, got[id], want, got)
		}
	}
	var reason string
	_ = d.Db.QueryRow(`SELECT retired_reason FROM register_devices WHERE register_id = 'r-g1'`).Scan(&reason)
	if reason != "stale_auto" {
		t.Fatalf("retired_reason = %q, want stale_auto", reason)
	}
	// throttled: the second call within 24 h does nothing even though r-g2 is now old
	if _, err := d.Db.Exec(`UPDATE sales SET created_at = ? WHERE id = 's2'`, time.Now().Add(-60*24*time.Hour).UTC().Format(time.RFC3339)); err != nil {
		t.Fatal(err)
	}
	if n := maybeAutoRetireRegisters(ctx, d, oldEntryRows("tA", "tC", "tU"), true); n != 0 {
		t.Fatalf("second call within 24 h retired %d, want 0", n)
	}
	// the notice is shown once
	if n := takeAutoRetireNotice(ctx, d); n != 1 {
		t.Fatalf("notice = %d, want 1", n)
	}
	if n := takeAutoRetireNotice(ctx, d); n != 0 {
		t.Fatalf("notice shown twice: %d", n)
	}
	// and it is reversible: Activate brings it back
}

// The checkbox posts, stores 1/0 and defaults off.
func TestAutoRetireToggleRoute(t *testing.T) {
	mux, d, m := ghostShop(t)
	if autoRetireSettings(t.Context(), d) {
		t.Fatal("auto-retire must default to off")
	}
	if body := getRegisters(t, mux, m); !strings.Contains(body, `action="/registers/auto-retire"`) || strings.Contains(body, "checked") {
		t.Fatal("the control must be shown unchecked on the main till")
	}
	if rec := postForm(mux, "/registers/auto-retire", url.Values{"enabled": {"1"}}, &m); rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "Saved.") {
		t.Fatalf("enable: %d %s", rec.Code, rec.Body.String())
	}
	if !autoRetireSettings(t.Context(), d) {
		t.Fatal("not stored")
	}
	if rec := postForm(mux, "/registers/auto-retire", url.Values{}, &m); rec.Code != http.StatusOK || autoRetireSettings(t.Context(), d) {
		t.Fatalf("disable: %d", rec.Code)
	}
	if rec := postForm(mux, "/registers/auto-retire", url.Values{"enabled": {"1"}}, &auth.User{ID: "c", Role: "cashier"}); rec.Code != http.StatusForbidden {
		t.Fatalf("cashier: %d, want 403", rec.Code)
	}
}

func TestAutoRetireDaysClamped(t *testing.T) {
	_, d, _ := ghostShop(t)
	for in, want := range map[string]int{"": 30, "3": 7, "9999": 365, "45": 45, "x": 30} {
		if in != "" {
			_ = d.Settings.Set(t.Context(), "registers.auto_retire_days", in)
		}
		if got := autoRetireDays(t.Context(), d); got != want {
			t.Fatalf("days %q -> %d, want %d", in, got, want)
		}
	}
}
