package pages

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/plugins"
)

// Test connection (ADR-0121 S5, ut-docs#5961): a manifest `settings_tests`
// entry draws a button on the settings form. Pressing it runs the normal
// save; only a successful save makes the page fire a second, separate request
// (POST .../tests/{test}/run) that asks the owning plugin through
// ui.settings.test.ask and swaps only that test's status line, so what the
// operator types meanwhile is never wiped. The ask carries the test id and
// nothing else: no setting value, no secret. A plugin that is slow, crashed,
// absent or answers badly is content ("Couldn't run the test"), never an HTTP
// error and never a rollback (ADR-0041 skip).

const settingsTestAskEvent = "ui.settings.test.ask"

// settingsTestTimeout is a var so tests can shorten it.
var settingsTestTimeout = 10 * time.Second

const settingsTestMaxAnswerBytes = 4 << 10

// settingsTestAsk is the payload. RecordID is never set here (#4158).
type settingsTestAsk struct {
	Test     string `json:"test"`
	RecordID string `json:"record_id,omitempty"`
}

// settingsTestView is one drawn button with its status line. Run makes the
// line fire the ask on load (the save just succeeded); Refused says the save
// was refused, so nothing was tested.
type settingsTestView struct {
	ID, Label, PluginID string
	Run, Refused        bool
}

// settingsTestResult is what the run endpoint renders. State is ok, failed
// or unavailable. Message is an already translated own-key message or "".
type settingsTestResult struct {
	ID, PluginID, State, Message, Field string
}

// drawnSettingsTest returns the declared, non-record test id, or nil.
func drawnSettingsTest(m *plugins.Manifest, id string) *plugins.ManifestSettingsTest {
	if m == nil || id == "" {
		return nil
	}
	for i := range m.SettingsTests {
		if t := &m.SettingsTests[i]; t.ID == id && !t.Record {
			return t
		}
	}
	return nil
}

// settingsTestViews is the drawn buttons, in manifest order (record tests
// belong to a list record's panel, #4158). run names the test to fire on load.
func settingsTestViews(m *plugins.Manifest, pluginID, locale, run string) []settingsTestView {
	if m == nil {
		return nil
	}
	var out []settingsTestView
	for i := range m.SettingsTests {
		t := &m.SettingsTests[i]
		if t.Record {
			continue
		}
		out = append(out, settingsTestView{ID: t.ID, Label: httpx.T(locale, t.Label), PluginID: pluginID, Run: run == t.ID})
	}
	return out
}

// markTestRefused flags the pressed test's line after a refused save.
func markTestRefused(views []settingsTestView, id string) []settingsTestView {
	for i := range views {
		if views[i].ID == id {
			views[i].Refused = true
		}
	}
	return views
}

// decodeSettingsTestAnswer is the strict decoder of the plugin's answer: any
// violation makes the whole answer malformed.
func decodeSettingsTestAnswer(raw []byte) (ok bool, message, field string, err error) {
	if len(raw) > settingsTestMaxAnswerBytes {
		return false, "", "", fmt.Errorf("answer is %d bytes, over %d", len(raw), settingsTestMaxAnswerBytes)
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	var ans *struct {
		OK      *bool   `json:"ok"`
		Message *string `json:"message"`
		Field   *string `json:"field"`
	}
	if err := dec.Decode(&ans); err != nil {
		return false, "", "", fmt.Errorf("answer is not a JSON object: %w", err)
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return false, "", "", errors.New("trailing data after the answer")
	}
	if ans == nil || ans.OK == nil {
		return false, "", "", errors.New(`answer has no boolean "ok"`)
	}
	if ans.Message != nil {
		message = *ans.Message
	}
	if ans.Field != nil {
		field = *ans.Field
	}
	return *ans.OK, message, field, nil
}

const settingsTestBlock = "plugin_settings_test_status"

// registerSettingsTest registers POST /api/plugins/{id}/settings/tests/{test}/run
// (not .../tests/{test}: that would overlap .../settings/{key}/options).
func registerSettingsTest(mux *http.ServeMux, d *common.Deps, repo *data.PluginRepo) {
	mux.HandleFunc("POST /api/plugins/{id}/settings/tests/{test}/run", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "plugin_management") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		// The ask makes the plugin run (and reach out): refuse a cross-site POST.
		if !sameOriginRequest(r) {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return
		}
		pluginID, testID := r.PathValue("id"), r.PathValue("test")
		_, installed, err := repo.GetInstalledPluginVersion(r.Context(), pluginID)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "plugins.error.server", "plugin_settings_test", err)
			return
		}
		if !installed {
			common.LocalizedError(w, r, http.StatusNotFound, "plugins.install.error.not_found")
			return
		}
		m, _, err := plugins.InstalledManifest(r.Context(), d.Db, pluginID)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "plugins.error.server", "plugin_settings_test", err)
			return
		}
		if _, _, _, resolved := secretSettingCheck(r.Context(), d, pluginID); !resolved {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "plugins.error.server", "plugin_settings_test",
				fmt.Errorf("plugin %s: manifest could not be resolved", pluginID))
			return
		}
		if drawnSettingsTest(m, testID) == nil {
			common.LocalizedError(w, r, http.StatusNotFound, "plugins.install.error.not_found")
			return
		}
		// The request carries no form values (hx-params=none); ignore any body.
		r.Body = http.MaxBytesReader(w, r.Body, 1<<10)
		rows, err := repo.ListPluginSettings(r.Context(), pluginID)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "plugins.error.server", "plugin_settings_test", err)
			return
		}
		locale := httpx.ResolveLocale(w, r)
		res := settingsTestResult{ID: testID, PluginID: pluginID, State: "unavailable"}

		raw, answered, err := boundedAskPlugin(r.Context(), d, "pages.settingsTestAsk", pluginID, settingsTestAskEvent,
			settingsTestAsk{Test: testID}, settingsTestTimeout)
		switch {
		case err != nil:
			logging.L().Infof("plugin settings: test %s/%s failed: %v", pluginID, testID, err)
		case !answered || len(raw) == 0:
			logging.L().Infof("plugin settings: test %s/%s was not answered", pluginID, testID)
		default:
			ok, message, field, derr := decodeSettingsTestAnswer(raw)
			if derr != nil {
				logging.L().Infof("plugin settings: test %s/%s answer is malformed: %v", pluginID, testID, derr)
				break
			}
			res.State = "failed"
			if ok {
				res.State = "ok"
			}
			if message != "" {
				if pluginOwnLocaleKeys(d, pluginID)[message] {
					res.Message = httpx.T(locale, message)
				} else {
					logging.L().Infof("plugin settings: test %s/%s message key %q is not the plugin's own", pluginID, testID, message)
				}
			}
			if !ok && field != "" {
				if decl := m.SettingDecl(field); decl != nil && decl.Type != plugins.SettingTypeList && decl.Type != plugins.SettingTypeMap &&
					!newSettingVisibility(m, nil, storedControlValues(rows)).skipped(field) {
					res.Field = field
				}
			}
		}
		httpx.RenderBlock("ui/pages/plugin_settings.html", settingsTestBlock, res)(w, r)
	})
}
