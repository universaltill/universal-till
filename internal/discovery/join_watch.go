package discovery

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/logging"
)

// Ambient main-till discovery on a standalone till (ut-docs#2721,
// ADR-0033 amendment).
//
// A new or reinstalled till with no main till yet (sync.primary_url empty)
// used to stay standalone until a manager remembered Settings → Tills →
// "Find a primary on this network". JoinWatch runs that same LAN browse on
// its own — on the first tick after launch and then at most once per
// MinBrowseInterval — and keeps the first candidate that is not this till
// itself. GET /ui/join-notice reads it to offer "Link this till".
//
// Deliberately NOT PrimaryWatch (primary_watch.go): that one re-finds a main
// till an already-paired replica holds a bearer for, so a candidate must
// prove itself before sync.primary_url changes. A standalone till has no
// bearer and nothing here changes any setting: the candidate is only shown.
// The join itself still goes through the existing pair-start → verification
// code → manager-PIN approval on the main till (pairing_join.go), so a wrong
// or rogue candidate fails exactly as it does for the manual search.

// JoinBannerDismissedSettingKey is set ("1") when a manager dismisses the
// "join this shop's main till" notice for good; the till then stops looking.
// The "sync." prefix keeps it per-till: admin-sync pulls never overwrite it
// (data.PerTillSettingPrefixes).
const JoinBannerDismissedSettingKey = "sync.join_banner_dismissed"

// JoinWatch caches the main till a standalone till could join. Safe for
// concurrent use: the pull loop ticks it while the notice handler reads
// Candidate. A nil *JoinWatch is valid and never has a candidate.
type JoinWatch struct {
	settings WatchSettings

	// Seams for tests.
	browse BrowseFunc
	now    func() time.Time

	mu         sync.Mutex
	lastBrowse time.Time
	candidate  Candidate
	found      bool
}

// NewJoinWatch builds a watch over settings that looks for a main till with
// browse (production passes Browse).
func NewJoinWatch(settings WatchSettings, browse BrowseFunc) *JoinWatch {
	return &JoinWatch{settings: settings, browse: browse, now: time.Now}
}

func (w *JoinWatch) get(ctx context.Context, key string) string {
	v, _, _ := w.settings.Get(ctx, key)
	return strings.TrimSpace(v)
}

// Tick is one beat of the pull loop on a till with no main till. It browses
// the LAN when this till is standalone, the notice was not dismissed, and
// MinBrowseInterval has passed since the last browse (the first tick always
// browses).
func (w *JoinWatch) Tick(ctx context.Context) {
	if w == nil {
		return
	}
	if w.get(ctx, "sync.primary_url") != "" || w.get(ctx, JoinBannerDismissedSettingKey) != "" {
		w.mu.Lock()
		w.candidate, w.found = Candidate{}, false
		w.mu.Unlock()
		return
	}
	w.mu.Lock()
	now := w.now()
	due := w.lastBrowse.IsZero() || now.Sub(w.lastBrowse) >= MinBrowseInterval
	if due {
		w.lastBrowse = now
	}
	w.mu.Unlock()
	if !due {
		return
	}

	cands, err := w.browse(ctx, browseTimeout)
	var best Candidate
	found := false
	if err != nil {
		if !errors.Is(err, context.Canceled) {
			logging.L().Infof("sync: could not look for a main till on this network (%v) — will look again in %s", err, MinBrowseInterval)
		}
	} else {
		// Read this till's own id AFTER the browse: the advertiser mints
		// it (TillID) before it first advertises, so any advertisement of
		// our own the browse could have heard is already covered here.
		self := w.get(ctx, TillIDSettingKey)
		for _, c := range cands {
			if c.BaseURL == "" || (self != "" && c.TillID == self) {
				continue
			}
			best, found = c, true
			break
		}
	}
	w.mu.Lock()
	w.candidate, w.found = best, found
	w.mu.Unlock()
}

// Candidate returns the main till found by the last browse, if any.
func (w *JoinWatch) Candidate() (Candidate, bool) {
	if w == nil {
		return Candidate{}, false
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.candidate, w.found
}
