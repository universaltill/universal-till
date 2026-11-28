package pages

import (
	"net/http"
	"sync"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#7173: the Tills & devices sections are lazy fragments of Settings.
// sectionRegistered remembers which muxes already carry them, so a test can
// ask for a section on any fixture without a duplicate-route panic.
var sectionRegistered sync.Map

// tillsSection GETs /ui/tills/section/{id} as u (nil = no user) on mux,
// registering the section routes on it first when the fixture lacks them.
func tillsSection(t *testing.T, mux *http.ServeMux, d *common.Deps, id string, u *auth.User) string {
	t.Helper()
	if _, done := sectionRegistered.LoadOrStore(mux, true); !done {
		func() {
			defer func() { _ = recover() }() // the fixture already registered them
			registerTillsTabs(mux, d)
		}()
	}
	rec := getAs(mux, "/ui/tills/section/"+id, u)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET section %s = %d", id, rec.Code)
	}
	return rec.Body.String()
}
