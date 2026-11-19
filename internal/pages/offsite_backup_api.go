package pages

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"html/template"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	qrcode "github.com/skip2/go-qrcode"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/offsitebackup"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/print"
)

// Off-site encrypted backup, till side (ut-docs#5066, ADR-0199).
//
//	GET  /ui/backup-cloud-card          Settings → Backups card (state + Upload now)
//	POST /api/offsite/now               run an upload round now
//	GET  /backups/cloud-restore         pick a cloud backup to restore
//	POST /api/offsite/recover/start     step-up, then request + download + stage
//	GET  /ui/backup-cloud-restore/status  poll: code + QR, progress, restart
//	POST /api/offsite/recover/cancel    stop before anything is staged
//	POST /api/offsite/link/start        step-up, then ask the owner to give this
//	                                    till the shop's backup key (same code + QR)
//	GET  /ui/offsite-nextstep           home Next-steps line (0 sales, entitled)
//
// Everything is manager/admin only (data_management): a cashier sees no
// control, and every route refuses her on the server too. Restoring replaces
// the till's data, so it asks for the manager PIN (checkStepUp) like
// POST /api/backup/restore. None of these routes sits on the sale path; the
// upload runs on the offsitebackup scheduler's own goroutine.

const cloudRestoreRegion = "#cloud-restore-msg"

func registerOffsiteBackup(mux *http.ServeMux, d *common.Deps) {
	if d.Db == nil || d.Cfg == nil {
		return
	}
	svc := func() *offsitebackup.Service { return offsitebackup.Shared(d.Cfg, d.Db) }
	posRepo := data.NewPOSRepo(d.Db)
	deny := func(w http.ResponseWriter, r *http.Request) bool {
		if canPerform(d, r, "data_management") {
			return false
		}
		common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
		return true
	}
	// quiet answers a hidden partial: an empty 200 for a viewer who may not
	// manage backups, so a polled slot just stays empty.
	quiet := func(w http.ResponseWriter, r *http.Request) bool {
		if canPerform(d, r, "data_management") {
			return false
		}
		w.WriteHeader(http.StatusOK)
		return true
	}
	auditFor := func(r *http.Request) func(string, map[string]any) {
		actor := getSessionUserID(r)
		return func(action string, payload map[string]any) {
			_ = posRepo.InsertAudit(context.Background(), nil, actor, "backup", "-", action, payload, time.Now().UTC().Format(time.RFC3339), "")
		}
	}

	card := func(w http.ResponseWriter, r *http.Request, msg string) {
		locale := httpx.ResolveLocale(w, r)
		s := svc()
		gate := s.Gate(r.Context())
		if gate == offsitebackup.GateUnpaired || gate == offsitebackup.GateNotMain {
			w.WriteHeader(http.StatusOK) // not this till's job
			return
		}
		get := func(k string) string { v, _, _ := s.Settings.Get(r.Context(), k); return strings.TrimSpace(v) }
		view := map[string]any{"entitled": gate == offsitebackup.GateOpen, "msg": msg}
		if gate == offsitebackup.GateOpen {
			view["choice"] = get(data.OffsiteKeyInCloudKey)
			view["blocked"] = get(data.OffsiteEscrowBlockedKey) == "1"
			view["linkCopy"] = get(data.OffsiteLinkCopyKey)
			view["hasKey"] = s.HasKey()
		}
		state := "waiting"
		switch {
		case gate == offsitebackup.GateNotEntitled:
			state = "not_in_plan"
		case get(data.OffsiteRefusedReasonKey) == offsitebackup.ReasonNeedsLink:
			state = "needs_link"
		case get(data.OffsiteRefusedReasonKey) != "":
			state = "refused"
			view["reason"] = get(data.OffsiteRefusedReasonKey)
		case get(data.OffsiteFailingSinceKey) != "":
			state = "failing"
			view["reason"] = get(data.OffsiteLastErrorKey)
		}
		if t, err := time.Parse(time.RFC3339, get(data.OffsiteLastSuccessAtKey)); err == nil {
			view["lastSuccess"] = httpx.FormatDateTime(t.Local(), locale)
			if state == "waiting" {
				state = "ok"
			}
		}
		view["state"] = state
		httpx.RenderPartial("ui/partials/backup_cloud_card.html", view)(w, r)
	}

	mux.HandleFunc("GET /ui/backup-cloud-card", func(w http.ResponseWriter, r *http.Request) {
		if quiet(w, r) {
			return
		}
		card(w, r, "")
	})

	// The owner's choice about a wrapped copy of the backup key in the cloud
	// (design section 15, E2). Manager/admin with the PIN; a cashier is refused.
	mux.HandleFunc("POST /api/offsite/key/choice", func(w http.ResponseWriter, r *http.Request) {
		if deny(w, r) {
			return
		}
		_ = r.ParseForm()
		choice := strings.TrimSpace(r.Form.Get("choice"))
		if choice != data.OffsiteKeyInCloudOn && choice != data.OffsiteKeyInCloudOff {
			http.Error(w, "invalid choice", http.StatusBadRequest)
			return
		}
		locale := httpx.ResolveLocale(w, r)
		elev := checkStepUp(d, r, "data_management", r.FormValue("override_pin"))
		if elev.Outcome != elevated {
			renderElevationPrompt(w, r, "/api/offsite/key/choice", "#backup-cloud-card",
				httpx.T(locale, "elevation.summary.backup_cloud_key_choice_"+choice),
				[]elevationHiddenField{{Name: "choice", Value: choice}}, elev)
			return
		}
		s := svc()
		if err := s.Settings.Set(r.Context(), data.OffsiteKeyInCloudKey, choice); err != nil {
			http.Error(w, "could not save", http.StatusInternalServerError)
			return
		}
		auditFor(r)("cloud_key_choice", map[string]any{"choice": choice})
		msg := ""
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		if err := s.Uploader.ApplyKeyChoice(ctx); err != nil {
			msg = httpx.T(locale, "backup.cloud.key.apply_failed")
		}
		card(w, r, msg)
	})

	// Start a new key: the way out for a shop whose key is held by neither
	// this till nor the cloud (E2/N1).
	mux.HandleFunc("POST /api/offsite/key/new", func(w http.ResponseWriter, r *http.Request) {
		if deny(w, r) {
			return
		}
		locale := httpx.ResolveLocale(w, r)
		elev := checkStepUp(d, r, "data_management", r.FormValue("override_pin"))
		if elev.Outcome != elevated {
			renderElevationPrompt(w, r, "/api/offsite/key/new", "#backup-cloud-card",
				httpx.T(locale, "elevation.summary.backup_cloud_key_new"), nil, elev)
			return
		}
		s := svc()
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		msg := ""
		switch err := s.Uploader.StartNewKey(ctx); {
		case err == nil:
			auditFor(r)("cloud_key_new_generation", map[string]any{})
			s.Scheduler.Trigger()
		case errors.Is(err, offsitebackup.ErrNotStuck):
			msg = httpx.T(locale, "backup.cloud.key.err.not_stuck")
		case errors.Is(err, offsitebackup.ErrNeedsOwnerApproval):
			msg = httpx.T(locale, "backup.cloud.key.err.owner_approval")
		default:
			msg = offsiteErrText(locale, offsiteErrCode(err))
		}
		card(w, r, msg)
	})

	// Enter the saved key (UTK1-...) to link this till: always available while
	// the till has no key. The text rides in a hidden field through the PIN prompt.
	mux.HandleFunc("POST /api/offsite/key/enter", func(w http.ResponseWriter, r *http.Request) {
		if deny(w, r) {
			return
		}
		_ = r.ParseForm()
		text := strings.TrimSpace(r.Form.Get("saved_key"))
		if len(text) == 0 || len(text) > 200 {
			http.Error(w, "invalid key", http.StatusBadRequest)
			return
		}
		locale := httpx.ResolveLocale(w, r)
		elev := checkStepUp(d, r, "data_management", r.FormValue("override_pin"))
		if elev.Outcome != elevated {
			renderElevationPrompt(w, r, "/api/offsite/key/enter", "#backup-cloud-card",
				httpx.T(locale, "elevation.summary.backup_cloud_key_enter"),
				[]elevationHiddenField{{Name: "saved_key", Value: text}}, elev)
			return
		}
		s := svc()
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		msg := ""
		switch err := s.Uploader.EnterSavedKey(ctx, text); {
		case err == nil:
			auditFor(r)("cloud_key_linked", map[string]any{"from": "saved_key"})
			s.Scheduler.Trigger()
			msg = httpx.T(locale, "backup.cloud.key.entered")
		case errors.Is(err, offsitebackup.ErrBadRecoveryKey):
			msg = httpx.T(locale, "backup.cloud.key.err.bad")
		case errors.Is(err, offsitebackup.ErrWrongKey):
			msg = httpx.T(locale, "backup.cloud.key.err.wrong")
		default:
			msg = offsiteErrText(locale, offsiteErrCode(err))
		}
		card(w, r, msg)
	})

	// While a restore or key link waits for the saved key (E5): the text or the
	// downloaded file. The restore itself already passed the PIN step-up.
	mux.HandleFunc("POST /api/offsite/recover/key", func(w http.ResponseWriter, r *http.Request) {
		if deny(w, r) {
			return
		}
		text := ""
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
			if err := r.ParseMultipartForm(64 << 10); err != nil {
				http.Error(w, "invalid upload", http.StatusBadRequest)
				return
			}
			text = strings.TrimSpace(r.FormValue("saved_key"))
			if f, _, err := r.FormFile("key_file"); err == nil {
				b, _ := io.ReadAll(io.LimitReader(f, 4096))
				f.Close()
				if text == "" {
					text = string(b)
				}
			}
		} else {
			_ = r.ParseForm()
			text = r.Form.Get("saved_key")
		}
		s := svc()
		locale := httpx.ResolveLocale(w, r)
		switch err := s.Restorer.SubmitKey(text); {
		case err == nil:
		case errors.Is(err, offsitebackup.ErrBadRecoveryKey):
			renderRestoreStatusMsg(w, r, s.Restorer.State(), httpx.T(locale, "backup.cloud.key.err.bad"))
			return
		case errors.Is(err, offsitebackup.ErrWrongKey):
			renderRestoreStatusMsg(w, r, s.Restorer.State(), httpx.T(locale, "backup.cloud.key.err.wrong"))
			return
		}
		renderRestoreStatus(w, r, s.Restorer.State())
	})

	// The key page: show, print and save the key (E4).
	mux.HandleFunc("GET /backups/cloud-key", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "data_management") {
			httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		locale := httpx.RequestLocale(r)
		httpx.Render("ui/pages/cloud_key.html", map[string]any{
			"title": httpx.T(locale, "page.title.cloud_key"), "theme": d.ResolvedTheme(r), "menuItems": d.MenuSnapshot(),
			"hasKey": svc().HasKey(),
		})(w, r)
	})

	keyStep := func(w http.ResponseWriter, r *http.Request, post, summaryKey string) (string, bool) {
		if deny(w, r) {
			return "", false
		}
		locale := httpx.ResolveLocale(w, r)
		elev := checkStepUp(d, r, "data_management", r.FormValue("override_pin"))
		if elev.Outcome != elevated {
			renderElevationPrompt(w, r, post, "#cloud-key-out", httpx.T(locale, summaryKey), nil, elev)
			return "", false
		}
		dek, gen, err := svc().Uploader.Keys.Load(svc().Uploader.StoreIDValue())
		if err != nil {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			fmt.Fprintf(w, `<p data-testid="cloud-key-none">%s</p>`, template.HTMLEscapeString(httpx.T(locale, "backup.cloud.key.none")))
			return "", false
		}
		defer func() {
			for i := range dek {
				dek[i] = 0
			}
		}()
		return offsitebackup.EncodeRecovery(gen, dek), true
	}
	mux.HandleFunc("POST /api/offsite/key/show", func(w http.ResponseWriter, r *http.Request) {
		str, ok := keyStep(w, r, "/api/offsite/key/show", "elevation.summary.backup_cloud_key_show")
		if !ok {
			return
		}
		_ = svc().Settings.Set(r.Context(), data.OffsiteKeyExportedAtKey, time.Now().UTC().Format(time.RFC3339))
		auditFor(r)("cloud_key_shown", map[string]any{}) // never the key
		w.Header().Set("Cache-Control", "no-store")
		httpx.RenderPartial("ui/partials/cloud_key_show.html", map[string]any{
			"key": str, "file": template.URL("data:text/plain;charset=utf-8," + url.QueryEscape(str+"\n")),
		})(w, r)
	})
	mux.HandleFunc("POST /api/offsite/key/print", func(w http.ResponseWriter, r *http.Request) {
		str, ok := keyStep(w, r, "/api/offsite/key/print", "elevation.summary.backup_cloud_key_print")
		if !ok {
			return
		}
		locale := httpx.ResolveLocale(w, r)
		ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
		defer cancel()
		cfg := printerConfig(ctx, d)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-store")
		if !cfg.Enabled() {
			fmt.Fprintf(w, `<span class="muted">%s</span>`, template.HTMLEscapeString(httpx.T(locale, "backup.cloud.key.print_off")))
			return
		}
		doc := print.Doc{
			StoreName: shopNameOrEmpty(ctx, d),
			Meta:      []string{httpx.T(locale, "backup.cloud.key.print_heading")},
			Lines:     keyPrintLines(str),
			Footer:    []string{httpx.T(locale, "backup.cloud.key.print_hint")},
			Charset:   cfg.Charset,
		}
		if err := print.PrintDoc(ctx, cfg, doc); err != nil {
			fmt.Fprintf(w, `<span class="muted">✗ %s</span>`, template.HTMLEscapeString(httpx.T(locale, "backup.cloud.key.print_failed")))
			return
		}
		fmt.Fprintf(w, `<span>✓ %s</span>`, template.HTMLEscapeString(httpx.T(locale, "backup.cloud.key.print_ok")))
	})

	// Upload now: the same in-place PIN re-auth as Back up now.
	mux.HandleFunc("POST /api/offsite/now", func(w http.ResponseWriter, r *http.Request) {
		elev := checkOrElevate(d, r, "data_management", r.FormValue("override_pin"))
		if elev.Outcome == needsElevation {
			renderElevationPrompt(w, r, "/api/offsite/now", "#backup-cloud-msg",
				httpx.T(httpx.ResolveLocale(w, r), "elevation.summary.backup_cloud_now"), nil, elev)
			return
		}
		svc().Scheduler.Trigger()
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprintf(w, `<span data-testid="backup-cloud-queued">✓ %s</span>`, template.HTMLEscapeString(httpx.T(httpx.ResolveLocale(w, r), "backup.cloud.queued")))
	})

	// The restore page. A cashier gets the standard 403 page, not a hidden
	// control alone.
	mux.HandleFunc("GET /backups/cloud-restore", func(w http.ResponseWriter, r *http.Request) {
		if !canPerform(d, r, "data_management") {
			httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
			return
		}
		locale := httpx.RequestLocale(r)
		s := svc()
		view := map[string]any{
			"title":     httpx.T(locale, "page.title.cloud_restore"),
			"theme":     d.ResolvedTheme(r),
			"menuItems": d.MenuSnapshot(),
			"active":    s.Restorer.Active() || s.Restorer.State().Phase != offsitebackup.PhaseIdle,
			"gateOpen":  s.Gate(r.Context()) != offsitebackup.GateUnpaired && s.Gate(r.Context()) != offsitebackup.GateNotMain,
		}
		if n, err := posRepo.CountCompletedSales(r.Context()); err == nil {
			view["sales"] = n
		}
		if view["gateOpen"] == true && view["active"] == false {
			ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
			defer cancel()
			list, err := s.Cloud.List(ctx)
			if err != nil {
				view["listError"] = offsiteErrCode(err)
			} else {
				rows := make([]map[string]any, 0, len(list.Backups))
				for _, b := range list.Backups {
					row := map[string]any{"id": b.BackupID, "tier": b.Tier, "size": formatBackupBytes(b.CiphertextBytes)}
					if t, err := time.Parse(time.RFC3339, b.TakenAt); err == nil {
						row["taken"] = httpx.FormatDateTime(t.Local(), locale)
					} else {
						row["taken"] = b.TakenAt
					}
					rows = append(rows, row)
				}
				view["backups"] = rows
				if list.DeleteAfter != nil {
					view["deleteAfter"] = *list.DeleteAfter
				}
			}
		}
		httpx.Render("ui/pages/cloud_restore.html", view)(w, r)
	})

	mux.HandleFunc("POST /api/offsite/recover/start", func(w http.ResponseWriter, r *http.Request) {
		if deny(w, r) {
			return
		}
		_ = r.ParseForm()
		id := strings.TrimSpace(r.Form.Get("backup_id"))
		if len(id) == 0 || len(id) > 64 {
			http.Error(w, "invalid backup", http.StatusBadRequest)
			return
		}
		locale := httpx.ResolveLocale(w, r)
		// Only a backup this shop's cloud lists can be restored: the id is
		// looked up, never trusted.
		s := svc()
		ctx, cancel := context.WithTimeout(r.Context(), 15*time.Second)
		defer cancel()
		list, err := s.Cloud.List(ctx)
		if err != nil {
			offsiteError(w, r, offsiteErrCode(err))
			return
		}
		var taken string
		found := false
		for _, b := range list.Backups {
			if b.BackupID == id {
				taken, found = b.TakenAt, true
			}
		}
		if !found {
			offsiteError(w, r, "not_found")
			return
		}
		when := taken
		if t, perr := time.Parse(time.RFC3339, taken); perr == nil {
			when = httpx.FormatDateTime(t.Local(), locale)
		}
		// The PIN step-up is the confirmation: this replaces the till's data.
		elev := checkStepUp(d, r, "data_management", r.FormValue("override_pin"))
		if elev.Outcome != elevated {
			renderElevationPrompt(w, r, "/api/offsite/recover/start", cloudRestoreRegion,
				fmt.Sprintf(httpx.T(locale, "elevation.summary.backup_cloud_restore"), when),
				[]elevationHiddenField{{Name: "backup_id", Value: id}}, elev)
			return
		}
		if err := s.Restorer.Start(id, taken, auditFor(r)); err != nil {
			offsiteError(w, r, "busy")
			return
		}
		renderRestoreStatus(w, r, s.Restorer.State())
	})

	// Link this till to the shop's backup key (K5): the cloud holds a key this
	// till does not. Manager/admin with the PIN; the same code + QR approval as
	// a restore, the same status region and poll.
	mux.HandleFunc("POST /api/offsite/link/start", func(w http.ResponseWriter, r *http.Request) {
		if deny(w, r) {
			return
		}
		locale := httpx.ResolveLocale(w, r)
		elev := checkStepUp(d, r, "data_management", r.FormValue("override_pin"))
		if elev.Outcome != elevated {
			renderElevationPrompt(w, r, "/api/offsite/link/start", "#backup-cloud-msg",
				httpx.T(locale, "elevation.summary.backup_cloud_link"), nil, elev)
			return
		}
		s := svc()
		if gate := s.Gate(r.Context()); gate != offsitebackup.GateOpen {
			offsiteError(w, r, "not_main_till")
			return
		}
		if err := s.Restorer.StartLink(auditFor(r)); err != nil {
			offsiteError(w, r, "busy")
			return
		}
		renderRestoreStatus(w, r, s.Restorer.State())
	})

	mux.HandleFunc("GET /ui/backup-cloud-restore/status", func(w http.ResponseWriter, r *http.Request) {
		if quiet(w, r) {
			return
		}
		renderRestoreStatus(w, r, svc().Restorer.State())
	})

	mux.HandleFunc("POST /api/offsite/recover/cancel", func(w http.ResponseWriter, r *http.Request) {
		if deny(w, r) {
			return
		}
		s := svc()
		s.Restorer.Cancel()
		// A restore stuck in a long import must not hold this request.
		s.Restorer.WaitTimeout(5 * time.Second)
		renderRestoreStatus(w, r, s.Restorer.State())
	})

	// Home Next-steps line: only on an empty, entitled main till, only for a
	// manager. Empty otherwise, so the placeholder <li> disappears.
	mux.HandleFunc("GET /ui/offsite-nextstep", func(w http.ResponseWriter, r *http.Request) {
		if quiet(w, r) {
			return
		}
		if svc().Gate(r.Context()) != offsitebackup.GateOpen {
			return
		}
		if has, err := posRepo.HasCompletedSales(r.Context()); err != nil || has {
			return
		}
		httpx.RenderPartial("ui/partials/offsite_nextstep.html", nil)(w, r)
	})
}

// offsiteErrCode maps a cloud error to a backup.cloud.err.* key suffix.
func offsiteErrCode(err error) string {
	var ae *offsitebackup.APIError
	if errors.As(err, &ae) {
		switch ae.Code {
		case "subscription_inactive", "cloud_backup_not_in_plan", "not_main_till", "rate_limited", "not_enabled", "expired":
			return ae.Code
		}
		if ae.Status == 429 {
			return "rate_limited"
		}
		if ae.Status == 402 {
			return "cloud_backup_not_in_plan"
		}
	}
	return "other"
}

// offsiteErrKeys maps an error code to its translated line.
var offsiteErrKeys = map[string]string{
	"disk":                     "backup.cloud.err.disk",
	"network":                  "backup.cloud.err.network",
	"server":                   "backup.cloud.err.server",
	"rate_limited":             "backup.cloud.err.rate_limited",
	"cloud_backup_not_in_plan": "backup.cloud.err.cloud_backup_not_in_plan",
	"subscription_inactive":    "backup.cloud.err.subscription_inactive",
	"not_main_till":            "backup.cloud.err.not_main_till",
	"too_large":                "backup.cloud.err.too_large",
	"not_enabled":              "backup.cloud.err.not_enabled",
	"expired":                  "backup.cloud.err.expired",
	"corrupt":                  "backup.cloud.err.corrupt",
	"other_shop":               "backup.cloud.err.other_shop",
	"newer":                    "backup.cloud.err.newer",
	"too_old":                  "backup.cloud.err.too_old",
	"consumed":                 "backup.cloud.err.consumed",
	"download":                 "backup.cloud.err.download",
	"busy":                     "backup.cloud.err.busy",
	"not_found":                "backup.cloud.err.not_found",
	"other":                    "backup.cloud.err.other",
}

// offsiteErrText is the translated line for code; unknown codes read as other.
func offsiteErrText(locale, code string) string {
	k, ok := offsiteErrKeys[code]
	if !ok {
		k = offsiteErrKeys["other"]
	}
	return httpx.T(locale, k)
}

func offsiteError(w http.ResponseWriter, r *http.Request, key string) {
	locale := httpx.ResolveLocale(w, r)
	msg := offsiteErrText(locale, key)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusConflict)
	fmt.Fprintf(w, `<p role="alert" data-testid="cloud-restore-error"><span class="row-warn-icon" aria-hidden="true">⚠</span>%s</p>`, template.HTMLEscapeString(msg))
}

// keyPrintLines splits the saved key into receipt-width lines of whole groups.
func keyPrintLines(key string) []print.Line {
	var lines []print.Line
	cur := ""
	for _, g := range strings.Split(key, "-") {
		if cur != "" && len(cur)+1+len(g) > print.Width-2 {
			lines = append(lines, print.Line{Name: cur})
			cur = ""
		}
		if cur != "" {
			cur += "-"
		}
		cur += g
	}
	if cur != "" {
		lines = append(lines, print.Line{Name: cur})
	}
	return lines
}

// renderRestoreStatus draws the status region for the restorer's state.
func renderRestoreStatus(w http.ResponseWriter, r *http.Request, st offsitebackup.RestoreState) {
	renderRestoreStatusMsg(w, r, st, "")
}

func renderRestoreStatusMsg(w http.ResponseWriter, r *http.Request, st offsitebackup.RestoreState, msg string) {
	locale := httpx.ResolveLocale(w, r)
	view := map[string]any{"msg": msg,
		"phase": st.Phase, "kind": st.Kind, "code": st.Code, "done": st.Done, "total": st.Total,
		"polling": st.Phase == offsitebackup.PhaseRequesting || st.Phase == offsitebackup.PhaseWaiting ||
			st.Phase == offsitebackup.PhaseDownloading || st.Phase == offsitebackup.PhaseChecking,
	}
	if t, err := time.Parse(time.RFC3339, st.TakenAt); err == nil {
		view["taken"] = httpx.FormatDateTime(t.Local(), locale)
	}
	if st.ErrCode != "" {
		view["errText"] = offsiteErrText(locale, st.ErrCode)
	}
	// The code is also the way in: a QR of the my. address, with the code on
	// it, so the owner can approve from a phone. The address comes from the
	// cloud; only the code is ever shown as text.
	if st.Phase == offsitebackup.PhaseWaiting && st.ApproveURL != "" && strings.HasPrefix(st.ApproveURL, "https://") {
		if png, err := qrcode.Encode(st.ApproveURL, qrcode.Medium, 200); err == nil {
			view["qr"] = template.URL("data:image/png;base64," + base64.StdEncoding.EncodeToString(png))
		}
	}
	if st.Phase == offsitebackup.PhaseReady {
		view["restartSupported"] = backupRestartSupported()
	}
	httpx.RenderPartial("ui/partials/cloud_restore_status.html", view)(w, r)
}
