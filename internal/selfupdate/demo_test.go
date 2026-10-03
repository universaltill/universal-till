package selfupdate

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/netaccess"
)

// ADR-0113 §1.6 (ut-docs#2795): a self-update download on the demo till is
// refused before anything leaves the process.
func TestDownloadDeniedInDemoMode(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("binary"))
	}))
	defer srv.Close()
	netaccess.SetDemo(true)
	t.Cleanup(func() { netaccess.SetDemo(false) })

	err := download(context.Background(), srv.URL+"/asset", filepath.Join(t.TempDir(), "asset"))
	if !errors.Is(err, netaccess.ErrDemoDenied) {
		t.Fatalf("download err = %v, want netaccess.ErrDemoDenied", err)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("server saw %d request(s) in demo mode, want 0", n)
	}
}
