package agentaction

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/db"
)

func op(user, session, req string) Operator {
	return Operator{UserID: user, Role: "manager", Session: session, Request: req}
}

func put(t *testing.T, s *Store, o Operator) (string, error) {
	t.Helper()
	a, _ := Lookup("stock.adjust.v1")
	return s.Put(context.Background(), nil, NewProposal("p", o, a, nil, nil, nil))
}

func TestRegistry_OnlyTheTwoActions_2838(t *testing.T) {
	if len(registry) != 2 {
		t.Fatalf("registry has %d actions, want 2 (catalog.item.create.v1, stock.adjust.v1)", len(registry))
	}
	for name, want := range map[string][2]string{
		"catalog.item.create.v1": {"catalog", "catalog_management"},
		"stock.adjust.v1":        {"stock", "stock_management"},
	} {
		a, ok := Lookup(name)
		if !ok || a.Area != want[0] || a.RolePermission != want[1] {
			t.Errorf("%s = %+v", name, a)
		}
	}
}

func TestParse_Bounds_2838(t *testing.T) {
	items := func(n int) string {
		p := make([]string, n)
		for i := range p {
			p[i] = fmt.Sprintf(`{"name":"I%d","price_minor":1}`, i)
		}
		return `{"items":[` + strings.Join(p, ",") + `]}`
	}
	if _, err := parseCreate([]byte(items(MaxBatch))); err != nil {
		t.Fatalf("%d items refused: %v", MaxBatch, err)
	}
	bad := []string{
		items(MaxBatch + 1), `{"items":[]}`, `{"items":[{"name":"","price_minor":1}]}`,
		`{"items":[{"name":"A"}]}`, `{"items":[{"name":"A\u0007","price_minor":1}]}`,
		// Invisible format characters (bidi override, zero-width) would let a
		// steered plugin show the operator a different name (#2838 review).
		`{"items":[{"name":"\u202Eklim taO","price_minor":1}]}`, `{"items":[{"name":"A\u200B","price_minor":1}]}`,
		`{"items":[{"name":"A","price_minor":1,"barcode":"x y"}]}`,
		`{"items":[{"name":"A","price_minor":1,"barcode":"1"},{"name":"B","price_minor":1,"barcode":"1"}]}`,
		`{"items":[{"name":"A","price_minor":1}]} {}`,
	}
	for _, b := range bad {
		if _, err := parseCreate([]byte(b)); !errors.Is(err, ErrInvalid) {
			t.Errorf("create %s: err = %v, want ErrInvalid", b, err)
		}
	}
	for _, b := range []string{`{"changes":[{"item_id":"a","delta":0}]}`, `{"changes":[{"item_id":"a"}]}`,
		`{"changes":[{"item_id":"a","delta":1},{"item_id":"a","delta":2}]}`, `{"changes":[{"item_id":"a","delta":1e9}]}`} {
		if _, err := parseAdjust([]byte(b)); !errors.Is(err, ErrInvalid) {
			t.Errorf("adjust %s: err = %v, want ErrInvalid", b, err)
		}
	}
}

func TestStore_SingleUseAndBound_2838(t *testing.T) {
	s := NewStore()
	id, err := put(t, s, op("u1", "s1", "r1"))
	if err != nil || len(id) != 32 {
		t.Fatalf("put = %q, %v", id, err)
	}
	ctx := context.Background()
	if s.Take(ctx, nil, id, "u2", "s1") != nil || s.Take(ctx, nil, id, "u1", "s2") != nil {
		t.Fatal("another user or session took the proposal")
	}
	if s.Take(ctx, nil, id, "u1", "s1") == nil {
		t.Fatal("the owner could not take it")
	}
	if s.Take(ctx, nil, id, "u1", "s1") != nil {
		t.Fatal("a proposal was used twice")
	}
}

func TestStore_Ceilings_2838(t *testing.T) {
	s := NewStore()
	for i := range MaxOpenPerRequest {
		if _, err := put(t, s, op("u", "s", "r")); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	if _, err := put(t, s, op("u", "s", "r")); !errors.Is(err, ErrQuota) {
		t.Fatalf("4th in one request: %v, want ErrQuota", err)
	}
	for i := MaxOpenPerRequest; i < MaxPerSessionPerHr; i++ {
		if _, err := put(t, s, op("u", "s", fmt.Sprint("r", i))); err != nil {
			t.Fatalf("put %d: %v", i, err)
		}
	}
	if _, err := put(t, s, op("u", "s", "fresh")); !errors.Is(err, ErrQuota) {
		t.Fatalf("21st in a session hour: %v, want ErrQuota", err)
	}
	// An hour later the session may propose again (the old ones expired).
	base := time.Now()
	s.now = func() time.Time { return base.Add(time.Hour + time.Second) }
	if _, err := put(t, s, op("u", "s", "later")); err != nil {
		t.Fatalf("after the window: %v", err)
	}
	g := NewStore()
	for i := range MaxOpenProposals {
		if _, err := put(t, g, op("u", fmt.Sprint("s", i), "r")); err != nil {
			t.Fatalf("global put %d: %v", i, err)
		}
	}
	if _, err := put(t, g, op("u", "another", "r")); !errors.Is(err, ErrQuota) {
		t.Fatalf("past the global cap: %v, want ErrQuota", err)
	}
}

func TestStore_ExpiryIsSweptAndAudited_2838(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "agent-*.db")
	if err != nil {
		t.Fatal(err)
	}
	f.Close()
	d, err := db.Open(f.Name())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { d.Close() })
	if _, err := d.Exec(`INSERT INTO users(id, username, display_name, pin_hash, role) VALUES ('u','u','u','x','manager')`); err != nil {
		t.Fatal(err)
	}
	s := NewStore()
	id, err := put(t, s, op("u", "s", "r"))
	if err != nil {
		t.Fatal(err)
	}
	base := time.Now()
	s.now = func() time.Time { return base.Add(ProposalTTL + time.Second) }
	if s.Peek(context.Background(), d.DB, id, "u", "s") != nil {
		t.Fatal("an expired proposal is still shown")
	}
	var n int
	if err := d.QueryRow(`SELECT COUNT(*) FROM audit_log WHERE action = 'agent.action.expired' AND actor_id = 'u'`).Scan(&n); err != nil || n != 1 {
		t.Fatalf("expired audit rows = %d (%v), want 1", n, err)
	}
}
