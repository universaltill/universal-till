package pages

import (
	"net/http"
	"sync"
	"time"

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
//
// Throttled (independent review): every edit changes the snapshot hash, so
// each check-in a storm of edits triggers rebuilds and pushes the whole
// catalogue. The first edit kicks at once; later ones within
// catalogKickGap of the last kick share one trailing kick (time.AfterFunc,
// no goroutine of ours), so the cloud still hears the last edit promptly.
func catalogKickWrap(h http.Handler, d *common.Deps) http.Handler {
	var k catalogKicker
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
		k.kick(d.CloudSyncNow)
	})
}

// catalogKickGap is the least time between two catalogue-edit kicks.
var catalogKickGap = 10 * time.Second

// catalogKicker spaces catalogue-edit kicks catalogKickGap apart, keeping
// at most one trailing kick pending.
type catalogKicker struct {
	mu      sync.Mutex
	last    time.Time
	pending bool
}

func (k *catalogKicker) kick(ch chan<- struct{}) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.pending {
		return // the trailing kick covers this edit too
	}
	wait := catalogKickGap - time.Since(k.last)
	if k.last.IsZero() || wait <= 0 {
		k.last = time.Now()
		sendKick(ch)
		return
	}
	k.pending = true
	time.AfterFunc(wait, func() {
		k.mu.Lock()
		k.pending = false
		k.last = time.Now()
		k.mu.Unlock()
		sendKick(ch)
	})
}

// sendKick never blocks: the channel has capacity 1, and a kick already
// pending covers this one.
func sendKick(ch chan<- struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
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
