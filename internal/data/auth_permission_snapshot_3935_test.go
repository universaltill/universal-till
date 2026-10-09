package data

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	sqlited "modernc.org/sqlite"
)

// genCountingConn counts prepared statements whose text mentions
// sync_admin_version (the generation read HasPermission does).
type genCountingConn struct {
	driver.Conn
	n *int64
}

func (c *genCountingConn) Prepare(q string) (driver.Stmt, error) {
	c.count(q)
	return c.Conn.Prepare(q)
}

func (c *genCountingConn) PrepareContext(ctx context.Context, q string) (driver.Stmt, error) {
	c.count(q)
	if pc, ok := c.Conn.(driver.ConnPrepareContext); ok {
		return pc.PrepareContext(ctx, q)
	}
	return c.Conn.Prepare(q)
}

func (c *genCountingConn) count(q string) {
	if strings.Contains(q, "sync_admin_version") && strings.HasPrefix(strings.ToUpper(strings.TrimSpace(q)), "SELECT") {
		atomic.AddInt64(c.n, 1)
	}
}

type genCountingConnector struct {
	dsn string
	n   *int64
}

func (c *genCountingConnector) Connect(context.Context) (driver.Conn, error) {
	conn, err := (&sqlited.Driver{}).Open(c.dsn)
	if err != nil {
		return nil, err
	}
	return &genCountingConn{Conn: conn, n: c.n}, nil
}
func (c *genCountingConnector) Driver() driver.Driver { return &sqlited.Driver{} }

// newSnapshotTestRepo returns an AuthRepo whose DB handle counts
// generation reads, plus a plain handle on the same file for raw writes.
func newSnapshotTestRepo(t *testing.T) (*AuthRepo, *sql.DB, *int64) {
	t.Helper()
	d := openMigratedDB(t, "auth_snapshot.db")
	var path string
	if err := d.DB.QueryRow(`SELECT file FROM pragma_database_list WHERE name = 'main'`).Scan(&path); err != nil {
		t.Fatal(err)
	}
	n := new(int64)
	cdb := sql.OpenDB(&genCountingConnector{
		dsn: fmt.Sprintf("file:%s?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)", path), n: n})
	t.Cleanup(func() { _ = cdb.Close() })
	return NewAuthRepo(cdb), d.DB, n
}

// ut-docs#3935: a request-scoped snapshot reads the generation once for
// any number of checks, and agrees with the row lookup.
func TestHasPermission_Snapshot_OneGenerationReadPerRequest(t *testing.T) {
	repo, _, n := newSnapshotTestRepo(t)
	ctx := WithPermissionSnapshot(context.Background())

	roles := []string{"admin", "manager", "cashier", "no-such-role"}
	actions := queryCol(t, repo, `SELECT action FROM permission_actions LIMIT 3`)
	actions = append(actions, "no-such-action")
	calls := 0
	for i := 0; calls < 10; i++ {
		role, action := roles[i%len(roles)], actions[(i/len(roles)+i)%len(actions)]
		got, err := repo.HasPermission(ctx, role, action)
		if err != nil {
			t.Fatal(err)
		}
		if want := rowLookupHasPermission(t, repo, role, action); got != want {
			t.Fatalf("HasPermission(%s,%s)=%v, rows say %v", role, action, got, want)
		}
		calls++
	}
	if got := atomic.LoadInt64(n); got != 1 {
		t.Fatalf("10 checks under one snapshot read the generation %d times, want 1", got)
	}
}

// A revoke applies on the next request (a new snapshot), never lingers.
func TestHasPermission_Snapshot_RevokeAppliesOnNextRequest(t *testing.T) {
	repo, raw, _ := newSnapshotTestRepo(t)
	ctx1 := WithPermissionSnapshot(context.Background())
	if ok, err := repo.HasPermission(ctx1, "manager", "refund"); err != nil || !ok {
		t.Fatalf("seed: manager should hold refund (ok=%v err=%v)", ok, err)
	}
	if _, err := raw.Exec(`UPDATE role_permissions SET granted = 0 WHERE role = 'manager' AND action = 'refund'`); err != nil {
		t.Fatal(err)
	}
	ctx2 := WithPermissionSnapshot(context.Background())
	if ok, err := repo.HasPermission(ctx2, "manager", "refund"); err != nil || ok {
		t.Fatalf("revoke not seen by next request (ok=%v err=%v)", ok, err)
	}
}

// Within one snapshot, an AuthRepo write drops the snapshot so a later
// check in the same request re-reads.
func TestHasPermission_Snapshot_OwnWriteVisibleInSameRequest(t *testing.T) {
	repo, _, n := newSnapshotTestRepo(t)
	ctx := WithPermissionSnapshot(context.Background())
	if ok, _ := repo.HasPermission(ctx, "manager", "refund"); !ok {
		t.Fatal("seed: manager should hold refund")
	}
	if err := repo.SetRolePermission(ctx, nil, "manager", "refund", false); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.HasPermission(ctx, "manager", "refund"); err != nil || ok {
		t.Fatalf("own write not visible in same request (ok=%v err=%v)", ok, err)
	}
	if got := atomic.LoadInt64(n); got != 2 {
		t.Fatalf("generation reads = %d, want 2 (before and after the write)", got)
	}
}

// No generation counter: the snapshot must not cache; rows are followed.
func TestHasPermission_Snapshot_NoGenerationRowNeverCached(t *testing.T) {
	repo, raw, _ := newSnapshotTestRepo(t)
	if _, err := raw.Exec(`DELETE FROM sync_admin_version`); err != nil {
		t.Fatal(err)
	}
	ctx := WithPermissionSnapshot(context.Background())
	if ok, err := repo.HasPermission(ctx, "manager", "refund"); err != nil || !ok {
		t.Fatalf("seed (ok=%v err=%v)", ok, err)
	}
	if _, err := raw.Exec(`UPDATE role_permissions SET granted = 0 WHERE role = 'manager' AND action = 'refund'`); err != nil {
		t.Fatal(err)
	}
	if ok, err := repo.HasPermission(ctx, "manager", "refund"); err != nil || ok {
		t.Fatalf("fallback path served a cached grant (ok=%v err=%v)", ok, err)
	}
}

// Without a snapshot the old shape holds: one generation read per call.
func TestHasPermission_NoSnapshot_ReadsGenerationEveryCall(t *testing.T) {
	repo, _, n := newSnapshotTestRepo(t)
	for i := 0; i < 10; i++ {
		if _, err := repo.HasPermission(context.Background(), "admin", "refund"); err != nil {
			t.Fatal(err)
		}
	}
	if got := atomic.LoadInt64(n); got != 10 {
		t.Fatalf("generation reads = %d, want 10", got)
	}
}

// The cloud-role writers drop the request's snapshot too, like
// SetRolePermission: each one, given a filled slot, must leave it empty.
func TestHasPermission_Snapshot_CloudRoleWritersDropSlot(t *testing.T) {
	repo, d := newAuthTestRepo(t)
	writers := map[string]func(ctx context.Context, tx *sql.Tx) error{
		"UpsertCloudRoleTx": func(ctx context.Context, tx *sql.Tx) error {
			return repo.UpsertCloudRoleTx(ctx, tx, "snapshot_test_role", "Snapshot test")
		},
		"ReplaceRoleGrantsTx": func(ctx context.Context, tx *sql.Tx) error {
			return repo.ReplaceRoleGrantsTx(ctx, tx, "manager", []string{"refund"})
		},
		"DeleteCloudRoleTx": func(ctx context.Context, tx *sql.Tx) error {
			return repo.DeleteCloudRoleTx(ctx, tx, "snapshot_test_role")
		},
	}
	for name, write := range writers {
		t.Run(name, func(t *testing.T) {
			ctx := WithPermissionSnapshot(context.Background())
			if _, err := repo.HasPermission(ctx, "manager", "refund"); err != nil {
				t.Fatal(err)
			}
			slot := ctx.Value(permSnapshotKey{}).(*permSnapshot)
			if slot.p.Load() == nil {
				t.Fatal("seed: the first check did not fill the slot")
			}
			tx, err := d.DB.Begin()
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = tx.Rollback() }()
			if err := write(ctx, tx); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			if slot.p.Load() != nil {
				t.Fatalf("%s left the request's permission snapshot filled", name)
			}
		})
	}
}
