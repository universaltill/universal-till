package updates

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/universaltill/universal-till/internal/netaccess"
)

// ADR-0113 §1.6 (ut-docs#2795): the "Check for updates" request on the demo
// till never leaves the process.
func TestCheckNowMakesNoRequestInDemoMode(t *testing.T) {
	resetState(t)
	var hits atomic.Int32
	withCheckServer(t, "1.0.0", func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte(`{"tag_name":"v9.9.9"}`))
	})
	netaccess.SetDemo(true)
	t.Cleanup(func() { netaccess.SetDemo(false) })

	CheckNow(context.Background())
	if n := hits.Load(); n != 0 {
		t.Fatalf("update check reached the server %d time(s) in demo mode, want 0", n)
	}
}
