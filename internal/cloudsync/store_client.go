package cloudsync

import (
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/netaccess"
)

// NewStoreClient returns an HTTP client for /v1/stores/* calls that carry a
// large body or answer slowly (the off-site backup's 8 MiB parts, ut-docs#5066):
// the same dedicated, netaccess-guarded transport settings as the sync calls,
// with the caller's own overall timeout instead of the 30 s of httpClient.
// Each call gets its own transport so a long upload never holds the sync
// loop's pooled connections.
func NewStoreClient(timeout time.Duration) *http.Client {
	return netaccess.NewClientWithTransport(timeout, newHTTPTransport())
}
