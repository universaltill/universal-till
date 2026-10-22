package pages

import (
	"net/http"

	"github.com/universaltill/universal-till/internal/pages/catalogsync"
	"github.com/universaltill/universal-till/internal/pages/common"
)

// catalogKickWrap asks the cloud sync loop for a check-in right after a
// catalogue edit succeeded on this till (ut-docs#4746). Without it a
// deactivate or edit made here waited for the next jittered tick (up to
// 144 s) before my. heard of it, while an edit made in my. reached the
// till at once through the cloud link's nudge. The kick is the same
// single-flight, coalescing one the link uses (d.CloudSyncNow, capacity 1):
// the check-in uploads the journal and pushes the snapshot as it always
// does. Only a mutating request that catalogsync.Routes lists and that
// answered below 400 kicks; a refused or failed edit changed nothing.
func catalogKickWrap(h http.Handler, d *common.Deps) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || d == nil || d.CloudSyncNow == nil {
			h.ServeHTTP(w, r)
			return
		}
		sw := &kickStatusWriter{ResponseWriter: w}
		h.ServeHTTP(sw, r)
		if sw.status != 0 && sw.status >= 400 {
			return
		}
		if _, _, known := catalogsync.Resolve(r.Method, r.URL.Path, nil); !known {
			return
		}
		select {
		case d.CloudSyncNow <- struct{}{}:
		default: // a check-in is already pending
		}
	})
}

// kickStatusWriter records the status code, 0 until one is written (an
// implicit 200).
type kickStatusWriter struct {
	http.ResponseWriter
	status int
}

func (s *kickStatusWriter) WriteHeader(code int) {
	if s.status == 0 {
		s.status = code
	}
	s.ResponseWriter.WriteHeader(code)
}

func (s *kickStatusWriter) Unwrap() http.ResponseWriter { return s.ResponseWriter }
