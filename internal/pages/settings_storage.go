package pages

import (
	"context"
	"errors"
	"fmt"
	"html"
	"html/template"
	"net/http"
	"path/filepath"
	"strconv"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/db"
	"github.com/universaltill/universal-till/internal/diskspace"
	"github.com/universaltill/universal-till/internal/housekeeping"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/issuereport"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/selfupdate"
	"github.com/universaltill/universal-till/internal/storageusage"
)

// ut-docs#3122: Settings → Storage, the read-only card. What uses space on
// this till and how long each kind of file is kept. Local files and settings
// only: no network call, so it reads the same offline.

// storageLayoutFor builds the measured layout; a var so tests point it at a
// temp dir.
var storageLayoutFor = func(d *common.Deps) storageusage.Layout {
	dataDir := diskSpaceDataDir(d)
	dbPath := ""
	if d.Cfg != nil {
		dbPath = d.Cfg.DBPath
	}
	return storageusage.Layout{
		DBPath:        dbPath,
		DataDir:       dataDir,
		BackupsDir:    db.BackupDirPath(dbPath),
		LogsDir:       filepath.Join(dataDir, "logs"),
		BugReportsDir: issuereport.PendingDir,
		UpdatesDir:    selfupdate.UpdatesDir(),
	}
}

// storageMeasure is the size walk; a var so tests can fail or slow it.
var storageMeasure = storageusage.Measure

type storageView struct {
	Failed    bool
	Usage     []storageUsageRow // six rows, storageusage order
	Skipped   bool              // some entries could not be read
	Disk      *storageDisk      // nil when the free space is unknown
	Files     []storageRuleRow  // the 7 rules about files
	SyncRules []storageRuleRow  // the 5 sync-history rules
}

type storageUsageRow struct{ Category, Label, Size string }

// storageDisk: Line has each number wrapped in <bdi> so it stays whole
// inside right-to-left text.
type storageDisk struct {
	Line template.HTML
	Low  bool
}

type storageRuleRow struct{ Kind, Label, Policy string }

// registerSettingsStorage: GET /ui/settings/storage, the Storage card's
// body, loaded when the card scrolls into view. Manager/admin only
// (requirePage "settings" → 403). Read-only, local, no network.
func registerSettingsStorage(mux *http.ServeMux, d *common.Deps) {
	mux.HandleFunc("GET /ui/settings/storage", func(w http.ResponseWriter, r *http.Request) {
		if !requirePage(d, w, r, "settings") {
			return
		}
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		const tpl = "ui/partials/settings_storage.html"
		locale := httpx.ResolveLocale(w, r)

		rep, err := storageMeasure(r.Context(), storageLayoutFor(d))
		if err != nil {
			if errors.Is(err, context.Canceled) || r.Context().Err() != nil {
				return // the client has gone
			}
			logging.L().Warnf("[Storage] measuring disk use: %v", err)
			// A text/html fragment is swapped into the card even on a 5xx
			// (app.js), so the manager sees the retry button, not a stuck
			// "Measuring…".
			w.WriteHeader(http.StatusInternalServerError)
			httpx.RenderPartial(tpl, storageView{Failed: true})(w, r)
			return
		}

		v := storageView{Skipped: rep.Skipped > 0}
		for _, row := range rep.Rows {
			v.Usage = append(v.Usage, storageUsageRow{
				Category: row.Category,
				Label:    httpx.T(locale, "settings.storage.cat."+row.Category),
				Size:     httpx.FormatBytes(row.Bytes, locale),
			})
		}

		if u, perr := diskSpaceChipProbe(diskSpaceDataDir(d)); perr == nil {
			v.Disk = &storageDisk{
				Line: storageDiskLine(locale, u),
				Low:  diskspace.Low(u),
			}
		}

		minDays, merr := data.ArchiveMinDaysForPruning(r.Context(), d.Db)
		if merr != nil {
			logging.L().Warnf("[Storage] resolve archive retention: %v (showing %d days)", merr, minDays)
		}
		rules := housekeeping.RetentionFor(housekeeping.Params{
			BackupDays:     data.BackupRetentionDays(r.Context(), d.Db),
			ArchiveMinDays: minDays,
		})
		v.Files, v.SyncRules = storageRuleRows(locale, rules)

		httpx.RenderPartial(tpl, v)(w, r)
	})
}

// storageDiskLine is the translated "Free space on this till: %s of %s" with
// each size in <bdi>. The format comes from en.json (escaped); the sizes are
// formatted and escaped here.
func storageDiskLine(locale string, u diskspace.Usage) template.HTML {
	format := html.EscapeString(httpx.T(locale, "settings.storage.disk"))
	free := "<bdi>" + html.EscapeString(httpx.FormatBytes(clampBytes(u.Free), locale)) + "</bdi>"
	total := "<bdi>" + html.EscapeString(httpx.FormatBytes(clampBytes(u.Total), locale)) + "</bdi>"
	return template.HTML(fmt.Sprintf(format, free, total)) // #nosec G203 -- format and values are escaped above
}

func clampBytes(n uint64) int64 {
	if n > 1<<62 {
		return 1 << 62
	}
	return int64(n)
}

// storageRuleRows formats each rule's policy:
// fmt.Sprintf of the kind's policy string, each arg through
// httpx.FormatDecimal (the keys use %s), split by r.InDatabase().
func storageRuleRows(locale string, rules []housekeeping.Rule) (files, sync []storageRuleRow) {
	for _, r := range rules {
		args := make([]any, len(r.Args))
		for i, a := range r.Args {
			args[i] = httpx.FormatDecimal(strconv.FormatInt(a, 10), locale)
		}
		row := storageRuleRow{
			Kind:   r.Kind,
			Label:  httpx.T(locale, "settings.storage.kind."+r.Kind),
			Policy: fmt.Sprintf(httpx.T(locale, "settings.storage.policy."+r.Kind), args...),
		}
		if r.InDatabase() {
			sync = append(sync, row)
		} else {
			files = append(files, row)
		}
	}
	return files, sync
}
