package plugins

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/tetratelabs/wazero/api"

	"github.com/universaltill/universal-till/internal/data"
)

// ut-docs#3697: records_forward / records_role / records_status /
// snapshot_set / snapshot_get (ADR-0149 §3, permission records:sync).

const (
	recPlugin = "com.test.records"
	recColOff = 0
	recIDOff  = 256
	recPayOff = 1024
	recDstOff = 140000
	recDstCap = 8192
)

func recordsManifest(pluginID string, collections, snapshots []string) string {
	c, _ := json.Marshal(collections)
	s, _ := json.Marshal(snapshots)
	return fmt.Sprintf(`{"id":%q,"name":"t","version":"1.0.0","entrypoint":"./plugin.wasm","runtime":"wasm","permissions":["records:sync"],"records_forward":{"collections":%s,"snapshots":%s}}`, pluginID, c, s)
}

// recordsEnv is a migrated DB, the plugin (optionally holding records:sync),
// its installed manifest and a guest memory.
type recordsEnv struct {
	t  *testing.T
	d  *sql.DB
	m  api.Module
	hs *hostState
}

func newRecordsEnv(t *testing.T, granted bool, collections, snapshots []string, replica bool) *recordsEnv {
	t.Helper()
	d := hostfnTestDB(t)
	seedPlugin(t, d, recPlugin)
	if granted {
		grantPerm(t, d, recPlugin, "records:sync")
	}
	withInstalledManifest(t, recPlugin, recordsManifest(recPlugin, collections, snapshots))
	if replica {
		if _, err := d.Exec(`INSERT INTO settings (key, value, updated_at) VALUES ('sync.primary_url','http://main', ?)`, time.Now().UTC()); err != nil {
			t.Fatal(err)
		}
	}
	return &recordsEnv{t: t, d: d, m: publishTestMemory(t), hs: &hostState{pluginID: recPlugin, db: d}}
}

func (e *recordsEnv) ctx() context.Context { return withHostState(context.Background(), e.hs) }

func (e *recordsEnv) put(off uint32, b []byte) {
	e.t.Helper()
	if len(b) > 0 && !e.m.Memory().Write(off, b) {
		e.t.Fatal("write guest memory")
	}
}

func (e *recordsEnv) forward(col, id string, payload []byte) int32 {
	e.put(recColOff, []byte(col))
	e.put(recIDOff, []byte(id))
	e.put(recPayOff, payload)
	return hostRecordsForward(e.ctx(), e.m, recColOff, uint32(len(col)), recIDOff, uint32(len(id)), recPayOff, uint32(len(payload)))
}

func (e *recordsEnv) status(col string) (int32, string) {
	e.put(recColOff, []byte(col))
	n := hostRecordsStatus(e.ctx(), e.m, recColOff, uint32(len(col)), recDstOff, recDstCap)
	if n < 0 {
		return n, ""
	}
	b, _ := e.m.Memory().Read(recDstOff, uint32(n))
	return n, string(b)
}

func (e *recordsEnv) snapSet(col string, payload []byte) int32 {
	e.put(recColOff, []byte(col))
	e.put(recPayOff, payload)
	return hostSnapshotSet(e.ctx(), e.m, recColOff, uint32(len(col)), recPayOff, uint32(len(payload)))
}

func (e *recordsEnv) snapGet(col string) (int32, string) {
	e.put(recColOff, []byte(col))
	n := hostSnapshotGet(e.ctx(), e.m, recColOff, uint32(len(col)), recDstOff, recDstCap)
	if n < 0 {
		return n, ""
	}
	b, _ := e.m.Memory().Read(recDstOff, uint32(n))
	return n, string(b)
}

func (e *recordsEnv) outbox() int {
	var n int
	if err := e.d.QueryRow(`SELECT COUNT(*) FROM plugin_record_outbox`).Scan(&n); err != nil {
		e.t.Fatal(err)
	}
	return n
}

func (e *recordsEnv) audits(action string) int {
	var n int
	_ = e.d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = ?`, action).Scan(&n)
	return n
}

func TestRecordsForward_ReplicaQueuesWithModulePluginID_3697(t *testing.T) {
	e := newRecordsEnv(t, true, []string{"entries"}, nil, true)
	if got := e.forward("entries", "id-1", []byte(`{"a":1}`)); got != 0 {
		t.Fatalf("records_forward on a replica = %d, want 0", got)
	}
	var pid, payload string
	if err := e.d.QueryRow(`SELECT plugin_id, payload FROM plugin_record_outbox`).Scan(&pid, &payload); err != nil {
		t.Fatal(err)
	}
	if pid != recPlugin || payload != `{"a":1}` {
		t.Fatalf("row = %q %q", pid, payload)
	}
	// idempotent
	if got := e.forward("entries", "id-1", []byte(`{"a":1}`)); got != 0 || e.outbox() != 1 {
		t.Fatalf("repeat = %d, rows %d, want 0 and 1", got, e.outbox())
	}
}

func TestRecordsForward_MainReturnsOneAndWritesNothing_3697(t *testing.T) {
	e := newRecordsEnv(t, true, []string{"entries"}, nil, false)
	if got := e.forward("entries", "id-1", []byte(`{}`)); got != 1 {
		t.Fatalf("records_forward on main = %d, want 1", got)
	}
	if e.outbox() != 0 {
		t.Fatal("main must not queue")
	}
}

func TestRecordsForward_Denials_3697(t *testing.T) {
	t.Run("no permission", func(t *testing.T) {
		e := newRecordsEnv(t, false, []string{"entries"}, nil, true)
		if got := e.forward("entries", "a", []byte(`{}`)); got != hostErrDenied {
			t.Fatalf("= %d, want denied", got)
		}
		if e.outbox() != 0 || e.audits("permission_denied") == 0 {
			t.Fatalf("denial must queue nothing and be audited (outbox %d)", e.outbox())
		}
	})
	t.Run("collection not declared", func(t *testing.T) {
		e := newRecordsEnv(t, true, []string{"entries"}, nil, true)
		if got := e.forward("other", "a", []byte(`{}`)); got != hostErrDenied {
			t.Fatalf("= %d, want denied", got)
		}
		if e.outbox() != 0 || e.audits("permission_denied") == 0 {
			t.Fatal("undeclared collection must be denied and audited")
		}
	})
	t.Run("manifest without records_forward", func(t *testing.T) {
		e := newRecordsEnv(t, true, nil, nil, true)
		if got := e.forward("entries", "a", []byte(`{}`)); got != hostErrDenied {
			t.Fatalf("= %d, want denied", got)
		}
	})
}

func TestRecordsForward_BadInput_3697(t *testing.T) {
	e := newRecordsEnv(t, true, []string{"entries"}, nil, true)
	big := []byte(`{"x":"` + strings.Repeat("y", 64<<10) + `"}`)
	justOK := []byte(`{"x":"` + strings.Repeat("y", 64<<10-8) + `"}`)
	for name, c := range map[string]struct {
		col, id string
		p       []byte
	}{
		"payload 64KiB+": {"entries", "a", big},
		"not an object":  {"entries", "a", []byte(`[1]`)},
		"not json":       {"entries", "a", []byte(`{nope`)},
		"empty payload":  {"entries", "a", nil},
		"bad collection": {"Entries", "a", []byte(`{}`)},
		"bad id":         {"entries", "a b", []byte(`{}`)},
		"empty id":       {"entries", "", []byte(`{}`)},
		"long id":        {"entries", strings.Repeat("a", 65), []byte(`{}`)},
	} {
		if got := e.forward(c.col, c.id, c.p); got != hostErrInvalid {
			t.Errorf("%s = %d, want -4", name, got)
		}
	}
	if e.outbox() != 0 {
		t.Fatal("bad input must queue nothing")
	}
	if got := e.forward("entries", "ok", justOK); got != 0 {
		t.Fatalf("payload at the cap = %d, want 0", got)
	}
}

func TestRecordsForward_PerEventCallCap_3697(t *testing.T) {
	e := newRecordsEnv(t, true, []string{"entries"}, nil, false)
	for i := 0; i < recordCallsPerEvent; i++ {
		if got := e.forward("entries", "a", []byte(`{}`)); got != 1 {
			t.Fatalf("call %d = %d, want 1", i, got)
		}
	}
	if got := e.forward("entries", "a", []byte(`{}`)); got != hostErrQuota {
		t.Fatalf("call over the cap = %d, want -5", got)
	}
}

func TestRecordsForward_OverflowStillQueuesAndAudits_3697(t *testing.T) {
	e := newRecordsEnv(t, true, []string{"entries"}, nil, true)
	// fill the per-plugin cap directly, then forward one more through the host fn
	now := time.Now().UTC()
	tx, _ := e.d.Begin()
	for i := 0; i < data.RecordMaxRowsPlugin; i++ {
		if _, err := tx.Exec(`INSERT INTO plugin_record_outbox (plugin_id, collection, record_id, payload, bytes, next_attempt_at, created_at) VALUES (?, 'entries', ?, '{}', 2, ?, ?)`,
			recPlugin, fmt.Sprintf("r%d", i), now.Format(time.RFC3339), now.Format(time.RFC3339)); err != nil {
			t.Fatal(err)
		}
	}
	_ = tx.Commit()
	if got := e.forward("entries", "newest", []byte(`{}`)); got != 0 {
		t.Fatalf("records_forward over the cap = %d, want 0", got)
	}
	var pending int
	_ = e.d.QueryRow(`SELECT COUNT(*) FROM plugin_record_outbox WHERE status = 'pending'`).Scan(&pending)
	if pending != data.RecordMaxRowsPlugin {
		t.Fatalf("pending = %d, want the cap %d", pending, data.RecordMaxRowsPlugin)
	}
	if e.audits("plugin_record_overflow") != 1 {
		t.Fatal("overflow must be audited")
	}
}

func TestRecordsRole_3697(t *testing.T) {
	for _, replica := range []bool{false, true} {
		e := newRecordsEnv(t, true, []string{"entries"}, nil, replica)
		n := hostRecordsRole(e.ctx(), e.m, recDstOff, recDstCap)
		b, _ := e.m.Memory().Read(recDstOff, uint32(n))
		want := "main"
		if replica {
			want = "replica"
		}
		if string(b) != want {
			t.Errorf("role(replica=%v) = %q, want %q", replica, b, want)
		}
	}
	e := newRecordsEnv(t, false, nil, nil, false)
	if got := hostRecordsRole(e.ctx(), e.m, recDstOff, recDstCap); got != hostErrDenied {
		t.Fatalf("role without records:sync = %d, want denied", got)
	}
}

func TestRecordsStatus_OwnRowsOnly_3697(t *testing.T) {
	e := newRecordsEnv(t, true, []string{"entries"}, nil, true)
	e.forward("entries", "mine", []byte(`{}`))
	now := time.Now().UTC().Format(time.RFC3339)
	_, _ = e.d.Exec(`INSERT INTO plugin_record_outbox (plugin_id, collection, record_id, payload, bytes, next_attempt_at, created_at) VALUES ('com.other','entries','theirs','{}',2,?,?)`, now, now)
	n, body := e.status("entries")
	if n < 0 {
		t.Fatalf("status = %d", n)
	}
	var rows []data.RecordStatusRow
	if err := json.Unmarshal([]byte(body), &rows); err != nil || len(rows) != 1 || rows[0].ID != "mine" || rows[0].Status != "pending" {
		t.Fatalf("status rows = %s (%v)", body, err)
	}
	if got, _ := e.status("undeclared"); got != hostErrDenied {
		t.Fatalf("status of an undeclared collection = %d, want denied", got)
	}
}

func TestSnapshot_MainSetsReplicaIgnoresAnyoneReads_3697(t *testing.T) {
	main := newRecordsEnv(t, true, nil, []string{"staff"}, false)
	if got := main.snapSet("staff", []byte(`[{"id":"1"}]`)); got != 0 {
		t.Fatalf("snapshot_set on main = %d, want 0", got)
	}
	if n, v := main.snapGet("staff"); n < 0 || v != `[{"id":"1"}]` {
		t.Fatalf("snapshot_get = %d %q", n, v)
	}
	if got, _ := main.snapGet("nope"); got != hostErrDenied {
		t.Fatalf("snapshot_get of an undeclared collection = %d, want denied", got)
	}
	if got := main.snapSet("other", []byte(`{}`)); got != hostErrDenied {
		t.Fatalf("snapshot_set of an undeclared collection = %d, want denied", got)
	}
	if got := main.snapSet("staff", []byte(`{nope`)); got != hostErrInvalid {
		t.Fatalf("non-JSON snapshot = %d, want -4", got)
	}
	big := []byte(`{"x":"` + strings.Repeat("y", 64<<10) + `"}`)
	if got := main.snapSet("staff", big); got != hostErrInvalid {
		t.Fatalf("oversized snapshot = %d, want -4", got)
	}

	rep := newRecordsEnv(t, true, nil, []string{"staff"}, true)
	if got := rep.snapSet("staff", []byte(`{}`)); got != 1 {
		t.Fatalf("snapshot_set on a replica = %d, want 1 (ignored)", got)
	}
	if n, _ := rep.snapGet("staff"); n != hostErrNotFound {
		t.Fatalf("snapshot_get on a replica that holds none = %d, want -1", n)
	}
	// a replica reads what the admin pull stored
	if _, err := data.NewPluginRecordsRepo(rep.d).SnapshotSet(context.Background(), recPlugin, "staff", []byte(`{"a":1}`), time.Now()); err != nil {
		t.Fatal(err)
	}
	if n, v := rep.snapGet("staff"); n < 0 || v != `{"a":1}` {
		t.Fatalf("replica snapshot_get = %d %q", n, v)
	}
}

func TestSnapshotGet_NeverReadsAnotherPluginsNamespace_3697(t *testing.T) {
	e := newRecordsEnv(t, true, nil, []string{"staff"}, false)
	_, _ = data.NewPluginRecordsRepo(e.d).SnapshotSet(context.Background(), "com.other", "staff", []byte(`{"secret":1}`), time.Now())
	if n, _ := e.snapGet("staff"); n != hostErrNotFound {
		t.Fatalf("got %d, want not found: another plugin's namespace is never read", n)
	}
}

func TestRecordsHostFns_InertWithoutImportSandboxState_3697(t *testing.T) {
	m := publishTestMemory(t)
	if got := hostRecordsForward(context.Background(), m, 0, 1, 2, 1, 4, 2); got != hostErrInternal {
		t.Fatalf("no host state = %d, want internal", got)
	}
}

// --- manifest: records_forward / records:sync ---

func parseRecordsManifest(t *testing.T, extra string) error {
	t.Helper()
	_, err := ParseManifest(strings.NewReader(manifestWith(extra)))
	return err
}

func TestManifestRecordsForward_Validation_3697(t *testing.T) {
	ok := `,"permissions":["records:sync","events:receive"],"records_forward":{"collections":["entries"],"snapshots":["staff","config"]},
		"hooks":[{"event":"plugin.records.received.ask","action":"records.apply"}]`
	if err := parseRecordsManifest(t, ok); err != nil {
		t.Fatalf("valid records_forward refused: %v", err)
	}
	names := func(n int) string {
		var q []string
		for i := 0; i < n; i++ {
			q = append(q, fmt.Sprintf("%q", fmt.Sprintf("c%d", i)))
		}
		return "[" + strings.Join(q, ",") + "]"
	}
	for name, extra := range map[string]string{
		"no permission":     `,"records_forward":{"collections":["entries"]}`,
		"hook without perm": `,"permissions":["events:receive"],"hooks":[{"event":"plugin.records.received.ask","action":"a"}]`,
		"upper-case name":   `,"permissions":["records:sync"],"records_forward":{"collections":["Entries"]}`,
		"leading digit":     `,"permissions":["records:sync"],"records_forward":{"collections":["1x"]}`,
		"33 chars":          `,"permissions":["records:sync"],"records_forward":{"collections":["` + strings.Repeat("a", 33) + `"]}`,
		"duplicate":         `,"permissions":["records:sync"],"records_forward":{"collections":["a","a"]}`,
		"nine collections":  `,"permissions":["records:sync"],"records_forward":{"collections":` + names(9) + `}`,
		"nine snapshots":    `,"permissions":["records:sync"],"records_forward":{"snapshots":` + names(9) + `}`,
		"dup snapshot":      `,"permissions":["records:sync"],"records_forward":{"snapshots":["a","a"]}`,
		"bad snapshot name": `,"permissions":["records:sync"],"records_forward":{"snapshots":["a-b"]}`,
	} {
		if err := parseRecordsManifest(t, extra); err == nil {
			t.Errorf("%s: manifest accepted, want refusal", name)
		}
	}
	// eight is the cap
	if err := parseRecordsManifest(t, `,"permissions":["records:sync"],"records_forward":{"collections":`+names(8)+`}`); err != nil {
		t.Errorf("eight collections refused: %v", err)
	}
}

func TestManifestRecordsForward_AbsentChangesNothing_3697(t *testing.T) {
	m, err := ParseManifest(strings.NewReader(manifestWith(``)))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(m)
	if strings.Contains(string(raw), "records_forward") {
		t.Fatalf("a manifest without records_forward must marshal without it (old signatures): %s", raw)
	}
}

func TestRecordsSyncPermissionKnown_3697(t *testing.T) {
	if !isKnownPermission("records:sync") {
		t.Fatal("records:sync must be a recognised permission")
	}
	if isKnownPermission("records:*") || isKnownPermission("records:sync:x") {
		t.Fatal("no wildcard or parameter form of records:sync")
	}
}

func TestIsSalePathEvent_RecordsReceivedIsNotReserved_3697(t *testing.T) {
	if isSalePathEvent(RecordsReceivedEvent) {
		t.Fatalf("%s must not take the reserved sale-path slot", RecordsReceivedEvent)
	}
	if !isSalePathEvent("tax.rate.ask") {
		t.Fatal("a sale-path ask must still be sale-path")
	}
}

// The Go SDK in a real wasip1 guest against the real host functions.
func TestRecordsGuestThroughTheSDK_3697(t *testing.T) {
	out := filepath.Join(t.TempDir(), "records_guest.wasm")
	cmd := exec.Command("go", "build", "-o", out, "./testdata/records_guest")
	cmd.Env = append(os.Environ(), "GOOS=wasip1", "GOARCH=wasm")
	if raw, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build records guest: %v\n%s", err, raw)
	}
	for _, replica := range []bool{true, false} {
		d := hostfnTestDB(t)
		seedPlugin(t, d, recPlugin)
		grantPerm(t, d, recPlugin, "storage")
		grantPerm(t, d, recPlugin, "records:sync")
		withInstalledManifest(t, recPlugin, recordsManifest(recPlugin, []string{"entries"}, []string{"staff"}))
		if replica {
			if _, err := d.Exec(`INSERT INTO settings (key, value, updated_at) VALUES ('sync.primary_url','http://main', ?)`, time.Now().UTC()); err != nil {
				t.Fatal(err)
			}
		}
		w := NewWasmRuntime(t.TempDir())
		if err := w.load(recPlugin, "1.0.0", out); err != nil {
			t.Fatalf("load: %v", err)
		}
		res := runGuestPayload(t, w, d, recPlugin, map[string]string{})
		wantRole := map[bool]string{true: "replica", false: "main"}[replica]
		if res["role"] != wantRole || res["queued"] != replica {
			t.Fatalf("replica=%v: role=%v queued=%v, want %s/%v (%v)", replica, res["role"], res["queued"], wantRole, replica, res)
		}
		if e, _ := res["undeclared_err"].(string); !strings.Contains(e, "permission denied") {
			t.Fatalf("undeclared collection err = %q, want permission denied", e)
		}
		if res["stored"] != !replica {
			t.Fatalf("replica=%v: snapshot stored=%v", replica, res["stored"])
		}
		if res["found"] != !replica {
			t.Fatalf("replica=%v: snapshot found=%v (a replica holds none until the pull)", replica, res["found"])
		}
		rows, _ := res["status"].([]any)
		if replica != (len(rows) == 1) {
			t.Fatalf("replica=%v: status rows = %v", replica, rows)
		}
		for _, k := range []string{"role_err", "forward_err", "status_err", "set_err", "get_err"} {
			if e, _ := res[k].(string); e != "" {
				t.Errorf("%s = %q", k, e)
			}
		}
	}
}

// Review M1: uninstalling a plugin drops its queued records, its inbox
// ledger and its snapshots; another plugin's rows stay.
func TestUninstallPlugin_DropsPluginRecords_3697(t *testing.T) {
	d := hostfnTestDB(t)
	seedPlugin(t, d, recPlugin)
	ctx := context.Background()
	now := time.Now().UTC()
	repo := data.NewPluginRecordsRepo(d)
	for _, p := range []string{recPlugin, "com.other"} {
		if _, err := repo.Enqueue(ctx, p, "entries", "x", []byte(`{}`), now); err != nil {
			t.Fatal(err)
		}
		if err := repo.InboxAdd(ctx, p, "entries", "y", "till-1", now); err != nil {
			t.Fatal(err)
		}
		if _, err := repo.SnapshotSet(ctx, p, "staff", []byte(`[]`), now); err != nil {
			t.Fatal(err)
		}
	}
	if err := UninstallPlugin(ctx, d, recPlugin); err != nil {
		t.Fatal(err)
	}
	for _, tbl := range []string{"plugin_record_outbox", "plugin_record_inbox", "plugin_snapshot"} {
		var mine, other int
		_ = d.QueryRow(`SELECT COUNT(*) FROM `+tbl+` WHERE plugin_id = ?`, recPlugin).Scan(&mine)
		_ = d.QueryRow(`SELECT COUNT(*) FROM ` + tbl + ` WHERE plugin_id = 'com.other'`).Scan(&other)
		if mine != 0 || other != 1 {
			t.Fatalf("%s after uninstall: uninstalled=%d (want 0) other=%d (want 1)", tbl, mine, other)
		}
	}
}
