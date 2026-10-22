package pages

import (
	"net/http"
	"net/http/httptest"
	"testing"

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
