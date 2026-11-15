package pages

import (
	"net/http"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/lantls"
)

// ADR-0114 §7 "Enforcement" (ut-docs#2737), end to end over a real
// same-port TLS + plain listener wired the way internal/server wires it: the
// main till records the first bearer request a till makes over pinned TLS,
// and from then on refuses that till's bearer on plain HTTP. A till that
// has never used TLS (paired before TLS, or to a main till without it) keeps
// plain HTTP unchanged.
func TestSyncTill_RefusesPlainHTTPOnceTheTillAuthenticatedOverTLS(t *testing.T) {
	d, _ := newSyncDepsWithPath(t, "primary.db")
	mux := http.NewServeMux()
	registerSyncAPI(mux, d)
	base, cert := serveTLSTill(t, mux)
	httpsBase, ok := lantls.HTTPSBase(base)
	if !ok {
		t.Fatalf("HTTPSBase(%q) not ok", base)
	}
	tills := data.NewTillsRepo(d.Db)
	if _, err := tills.InsertTill(t.Context(), "Pinned till", hashBearer("bearer-pinned")); err != nil {
		t.Fatal(err)
	}
	if _, err := tills.InsertTill(t.Context(), "Unpinned till", hashBearer("bearer-unpinned")); err != nil {
		t.Fatal(err)
	}

	ping := func(c *http.Client, base, bearer string) int {
		t.Helper()
		req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, base+"/api/sync/ping", nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+bearer)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("GET %s/api/sync/ping: %v", base, err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	plain := &http.Client{Timeout: 5 * time.Second}
	pinned := lantls.NewPinnedClient(5*time.Second, cert.Pin()).Client

	// Before any TLS request, plain HTTP still works for both (mixed
	// versions, ADR-0114 §11).
	if got := ping(plain, base, "bearer-pinned"); got != http.StatusOK {
		t.Fatalf("plain ping before pinning = %d, want 200", got)
	}
	// The till's first pinned TLS request is accepted and recorded.
	if got := ping(pinned, httpsBase, "bearer-pinned"); got != http.StatusOK {
		t.Fatalf("pinned TLS ping = %d, want 200", got)
	}
	// From now on its bearer is refused on plain HTTP...
	if got := ping(plain, base, "bearer-pinned"); got != http.StatusUnauthorized {
		t.Fatalf("plain ping after the till pinned = %d, want 401", got)
	}
	// ...but still accepted over TLS,
	if got := ping(pinned, httpsBase, "bearer-pinned"); got != http.StatusOK {
		t.Fatalf("pinned TLS ping after pinning = %d, want 200", got)
	}
	// and a till that never used TLS keeps plain HTTP.
	if got := ping(plain, base, "bearer-unpinned"); got != http.StatusOK {
		t.Fatalf("plain ping from an unpinned till = %d, want 200", got)
	}

	// The record is persistent (a main-till restart must not reopen plain
	// HTTP for a pinned till): a fresh repo over the same DB still refuses.
	row, ok, err := data.NewTillsRepo(d.Db).TillByBearerHashAt(t.Context(), hashBearer("bearer-pinned"), time.Now())
	if err != nil || !ok || !row.TLSPinned {
		t.Fatalf("stored record: TLSPinned=%v ok=%v err=%v; want pinned", row.TLSPinned, ok, err)
	}
}
