package pages

import (
	"context"
	"html/template"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/releasenotes"
)

// In-app release notes (ut-docs#3091): Settings → About shows the running
// version, when this till first ran it, and the embedded "What's new"
// (internal/releasenotes); after an update a status-bar chip announces the
// new version once, to users who can open Settings, never on the self-order
// kiosk (httpx's releasenoticeversion func).

// aboutRecentNotes is how many releases the About card lists: the running
// one plus the previous four.
const aboutRecentNotes = 5

// settingsKV is the slice of *settings.Store the version bookkeeping needs.
type settingsKV interface {
	Get(ctx context.Context, key string) (string, bool, error)
	Set(ctx context.Context, key, value string) error
}

// recordRunningVersion runs once at boot. A version change records the new
// version and when it first ran; a fresh install (setup not completed yet)
// also marks its own notes as seen, so a new shop is not told it was
// "updated" on day one. It then publishes whether the chip should show:
// the running version has notes and they haven't been seen. Errors are
// logged, never fatal — this is a notice, not part of selling.
func recordRunningVersion(ctx context.Context, store settingsKV, lib *releasenotes.Library, running string, now time.Time) {
	log := logging.L()
	stored, _, err := store.Get(ctx, data.AppVersionSettingsKey)
	if err != nil {
		log.Warnf("release notes: read %s: %v", data.AppVersionSettingsKey, err)
		return
	}
	if stored != running {
		if err := store.Set(ctx, data.AppVersionSettingsKey, running); err != nil {
			log.Warnf("release notes: record running version: %v", err)
		}
		if err := store.Set(ctx, data.AppVersionFirstRunAtSettingsKey, now.UTC().Format(time.RFC3339)); err != nil {
			log.Warnf("release notes: record first run: %v", err)
		}
		// A version change before setup is finished counts as a fresh
		// install: no "Updated to …" chip on day one. Accepted trade-off: a
		// shop that abandons the wizard and is auto-updated before finishing
		// it never sees the chip for that one version (the notes stay under
		// Settings → About).
		if done, _, _ := store.Get(ctx, "setup.completed"); done != "true" {
			if tag := releasenotes.Tag(running); tag != "" {
				if err := store.Set(ctx, data.ReleaseNotesSeenVersionSettingsKey, tag); err != nil {
					log.Warnf("release notes: seed seen version: %v", err)
				}
			}
		}
	}
	releasenotes.SetNoticeVersion(pendingNotice(ctx, store, lib, running))
}

// pendingNotice is the version the chip should announce, or "".
func pendingNotice(ctx context.Context, store settingsKV, lib *releasenotes.Library, running string) string {
	tag := releasenotes.Tag(running)
	if tag == "" || !lib.Has(tag) {
		return ""
	}
	if seen, _, err := store.Get(ctx, data.ReleaseNotesSeenVersionSettingsKey); err != nil || seen == tag {
		return ""
	}
	return tag
}

// markReleaseNotesSeen records that the running version's notes were seen
// and clears the chip.
func markReleaseNotesSeen(ctx context.Context, store settingsKV, running string) error {
	tag := releasenotes.Tag(running)
	if tag == "" {
		releasenotes.SetNoticeVersion("")
		return nil
	}
	if err := store.Set(ctx, data.ReleaseNotesSeenVersionSettingsKey, tag); err != nil {
		return err
	}
	releasenotes.SetNoticeVersion("")
	return nil
}

// releaseNoteView is one release in the About card.
type releaseNoteView struct {
	Version    string
	DateText   string
	HTML       template.HTML
	Translated bool
}

// aboutView is the About card's data: installed version, first run (RFC
// 3339, rendered by the `datetime` template func) and recent notes.
func aboutView(ctx context.Context, store settingsKV, locale string) map[string]any {
	firstRun, _, _ := store.Get(ctx, data.AppVersionFirstRunAtSettingsKey)
	notes := releasenotes.Builtin().Recent(locale, buildinfo.Version, aboutRecentNotes)
	views := make([]releaseNoteView, 0, len(notes))
	for _, n := range notes {
		views = append(views, releaseNoteView{
			Version:    n.Version,
			DateText:   httpx.FormatDate(n.Date, locale),
			HTML:       n.HTML,
			Translated: n.Translated,
		})
	}
	return map[string]any{"FirstRunAt": firstRun, "Notes": views}
}

// registerReleaseNotes wires the chip's dismiss. Called from registerSettings
// so every harness that serves /settings serves this too.
func registerReleaseNotes(mux *http.ServeMux, d *common.Deps) {
	// POST /api/release-notes/seen — the chip's ×. Same gate as the rest of
	// Settings (a cashier never sees the chip; a direct call is refused).
	// The empty 200 body replaces the chip (hx-swap="outerHTML"). The chip's
	// link posts here too, with ?open=1: opening the notes from the chip
	// counts as seen, and HX-Redirect then takes the manager to Settings →
	// About. Marking seen is a POST on purpose — a GET that mutated would be
	// cleared by any prefetch or link preview (review, 2026-09-28).
	mux.HandleFunc("POST /api/release-notes/seen", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "settings") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		if err := markReleaseNotesSeen(r.Context(), d.Settings, buildinfo.Version); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "common.error.server", "release notes: mark seen", err)
			return
		}
		if r.URL.Query().Get("open") == "1" {
			w.Header().Set("HX-Redirect", "/settings#settings-about")
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusOK)
	})
}
