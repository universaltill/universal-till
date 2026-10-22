package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/pages/common"
)

// ut-docs#4746: a catalogue edit made on the till reaches the cloud within
// seconds, not at the next 2-minute check-in: a successful catalogue
// mutation kicks the cloud sync loop.
func TestCatalogKickWrap(t *testing.T) {
	status := http.StatusOK
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(status) })
	cases := []struct {
		name, method, path string
		status             int
		kicked             bool
	}{
		{"item deactivate", http.MethodPost, "/api/catalog/item/deactivate", 200, true},
		{"item deactivate refused", http.MethodPost, "/api/catalog/item/deactivate", 403, false},
		{"item deactivate failed", http.MethodPost, "/api/catalog/item/deactivate", 500, false},
		{"a read", http.MethodGet, "/api/catalog/item/deactivate", 200, false},
		{"not a catalogue route", http.MethodPost, "/api/pos/add", 200, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			d := &common.Deps{CloudSyncNow: make(chan struct{}, 1)}
			status = c.status
			h := catalogKickWrap(inner, d)
			h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(c.method, c.path, nil))
			got := len(d.CloudSyncNow) == 1
			if got != c.kicked {
				t.Fatalf("kicked = %v, want %v", got, c.kicked)
			}
		})
	}
	// A second edit while a kick is pending neither blocks nor queues twice.
	d := &common.Deps{CloudSyncNow: make(chan struct{}, 1)}
	status = 200
	h := catalogKickWrap(inner, d)
	for range 3 {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/catalog/item/deactivate", nil))
	}
	if len(d.CloudSyncNow) != 1 {
		t.Fatalf("pending kicks = %d, want 1", len(d.CloudSyncNow))
	}
	// No loop (nil channel) is fine.
	catalogKickWrap(inner, &common.Deps{}).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/catalog/item/deactivate", nil))
}

// Independent review (ut-docs#4746): an edit storm must not turn into
// back-to-back check-ins (each one rebuilds and pushes the whole catalogue
// snapshot, since every edit changes its hash). The first edit kicks at
// once; edits within catalogKickGap of the last kick share one trailing
// kick, so the last edit still reaches the cloud promptly.
func TestCatalogKickWrap_StormIsThrottledWithATrailingKick(t *testing.T) {
	old := catalogKickGap
	catalogKickGap = 150 * time.Millisecond
	t.Cleanup(func() { catalogKickGap = old })
	inner := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	d := &common.Deps{CloudSyncNow: make(chan struct{}, 1)}
	h := catalogKickWrap(inner, d)
	edit := func() {
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodPost, "/api/catalog/item/deactivate", nil))
	}
	edit()
	select {
	case <-d.CloudSyncNow: // the loop takes it at once
	default:
		t.Fatal("first edit did not kick at once")
	}
	for range 20 {
		edit()
	}
	if len(d.CloudSyncNow) != 0 {
		t.Fatal("an edit within the gap kicked straight away: a storm would run check-ins back to back")
	}
	kicks := 0
	deadline := time.After(catalogKickGap * 4)
	for done := false; !done; {
		select {
		case <-d.CloudSyncNow:
			kicks++
		case <-deadline:
			done = true
		}
	}
	if kicks != 1 {
		t.Fatalf("trailing kicks = %d, want exactly 1 for the 20 edits in the gap", kicks)
	}
}
