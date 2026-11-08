package data

import (
	"context"
	"testing"
)

// ut-docs#5863: %, _ and \ in the erasure picker's q match literally.
func TestSearchCustomers_LikeWildcardsLiteral_5863(t *testing.T) {
	d := b8OpenDB(t, "customer-search-like-5863.db")
	mustExec(t, d, `INSERT INTO customers (id, name, phone, email) VALUES
		('c-a', 'Alpha One', '+44 100%', 'a_b@example.com'),
		('c-b', 'Beta Two', '+44 1001', 'axb@example.com'),
		('c-c', 'Gamma Three', 'C:\x', 'g@example.com')`)
	repo := NewPOSRepo(d.DB)
	ctx := context.Background()
	for q, want := range map[string]string{"100%": "c-a", "a_b": "c-a", `C:\`: "c-c"} {
		got, err := repo.SearchCustomers(ctx, q, 25)
		if err != nil {
			t.Fatalf("%q: %v", q, err)
		}
		if len(got) != 1 || got[0].ID != want {
			t.Fatalf("%q: want [%s], got %+v", q, want, got)
		}
	}
	// A bare wildcard must not list everyone.
	for _, q := range []string{"%", "_"} {
		got, err := repo.SearchCustomers(ctx, q, 25)
		if err != nil {
			t.Fatal(err)
		}
		if len(got) > 1 {
			t.Fatalf("%q matched %d customers, want literal match only", q, len(got))
		}
	}
}
