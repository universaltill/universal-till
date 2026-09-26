package pages

import (
	"context"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/fxlevel"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/settings"
)

// readFxSignals is a test seam over the host signals (ADR-0119 §4).
var readFxSignals = fxlevel.ReadSignals

// resolveEffectsLevel returns the visual effects level this till renders
// (ADR-0119, ut-docs#2859): the operator's explicit level, or for "auto"
// (the default, and any unknown value) the stored host detection.
//
// Host detection re-runs when the stored hardware fingerprint differs from
// this host's, or no valid detection is stored — a fresh install, a
// replica that just joined (ApplyReplicaIdentity drops the main till's
// rows), a moved disk or a RAM upgrade. It runs whatever the level, so
// Settings → Display can show what Auto would pick next to an explicit
// choice, but it never overwrites that choice. It is two small local file
// reads: no network, nothing that can hold up the sale screen. A store
// error degrades to the fresh detection (or Full) and is only logged.
func resolveEffectsLevel(ctx context.Context, store *settings.Store) string {
	vals, err := store.GetByPrefix(ctx, fxlevel.SettingsPrefix)
	if err != nil {
		logging.L().Warnf("settings: read %s*: %v", fxlevel.SettingsPrefix, err)
		vals = map[string]string{}
	}
	sig := readFxSignals()
	fp := sig.Fingerprint()
	detected := vals[fxlevel.KeyDetected]
	if vals[fxlevel.KeyFingerprint] != fp || !fxlevel.ValidResolved(detected) {
		res := fxlevel.Detect(sig)
		detected = res.Level
		if err := store.SetMany(ctx, map[string]string{
			fxlevel.KeyDetected:    res.Level,
			fxlevel.KeyReason:      res.Reason,
			fxlevel.KeyFingerprint: fp,
		}); err != nil {
			logging.L().Warnf("settings: store effects detection: %v", err)
		} else {
			logging.L().Infof("effects level: host detection %s (%s)", res.Level, res.Reason)
		}
	}
	if lvl := vals[fxlevel.KeyLevel]; fxlevel.ValidResolved(lvl) {
		return lvl
	}
	return detected
}

// effectsReasonPart is one reason token put into words on Settings →
// Display: an i18n key, with Arg for a printf %d when it carries a count.
type effectsReasonPart struct {
	Key string
	Arg int
}

// effectsLevelView is what the Display card's effects selector renders.
type effectsLevelView struct {
	Level       string // the setting: auto|full|balanced|light
	Detected    string // what Auto resolves to: full|balanced|light, "" if never detected
	ReasonParts []effectsReasonPart
}

func effectsLevelViewFrom(all map[string]string) effectsLevelView {
	v := effectsLevelView{Level: all[fxlevel.KeyLevel]}
	if !fxlevel.ValidSetting(v.Level) {
		v.Level = fxlevel.Auto
	}
	if d := all[fxlevel.KeyDetected]; fxlevel.ValidResolved(d) {
		v.Detected = d
	}
	for _, tok := range fxlevel.ParseReason(all[fxlevel.KeyReason]) {
		v.ReasonParts = append(v.ReasonParts, effectsReasonPart{
			Key: "settings.display.effects_reason_" + tok.Name,
			Arg: tok.Value,
		})
	}
	return v
}

// registerEffectsLevel wires POST /api/settings/effects-level: this till's
// visual effects level (ADR-0119 §6). Same shape and role as
// /api/settings/ui-scale — a signed-in session, no elevation; per-till,
// never synced. The new level takes effect on the next navigation: the
// shell signature carries it, so that navigation is one full load.
func registerEffectsLevel(mux *http.ServeMux, store *settings.Store) {
	mux.HandleFunc("POST /api/settings/effects-level", func(w http.ResponseWriter, r *http.Request) {
		_ = r.ParseForm()
		level := strings.TrimSpace(r.Form.Get("level"))
		if !fxlevel.ValidSetting(level) {
			http.Error(w, "level must be auto, full, balanced or light", http.StatusBadRequest)
			return
		}
		if err := store.Set(r.Context(), fxlevel.KeyLevel, level); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		httpx.InitEffectsLevel(resolveEffectsLevel(r.Context(), store))
		w.WriteHeader(http.StatusNoContent)
	})
}
