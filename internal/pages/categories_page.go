package pages

import (
	"errors"
	"fmt"
	"image"
	"io"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/catimport"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/imaging"
	"github.com/universaltill/universal-till/internal/pages/catalog"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pages/itemsnav"
	"github.com/universaltill/universal-till/internal/paths"
)

// categoryUploadMaxBytes bounds the category dialog's whole multipart body
// (ut-docs#2500): the same 10MB photo cap the catalog item upload uses,
// plus headroom for the dialog's plain fields.
const categoryUploadMaxBytes = 11 << 20

// categoryThumbURL / categoryThumbFile are where an uploaded category photo
// lives (ut-docs#2500): the stored image_path and its file under the
// stable data dir (paths.Data, so it survives a self-update), the same
// shape as an item's /public/assets/items/<id>/thumb.png.
func categoryThumbURL(id string) string { return "/public/assets/categories/" + id + "/thumb.png" }

func categoryThumbFile(id string) string {
	return filepath.Join(paths.Data("public", "assets", "categories", id), "thumb.png")
}

// safeCategoryID refuses an id that could escape the categories asset
// directory before it is ever joined into a filesystem path — the same
// guard the item/variant upload handlers apply to their ids.
func safeCategoryID(id string) bool {
	return id != "" && !strings.ContainsAny(id, "/\\.")
}

// removeCategoryUpload deletes a category's uploaded photo once it is
// superseded (a built-in icon picked, or "No image"). Best-effort: a
// missing file is the normal case.
func removeCategoryUpload(id string) {
	if err := os.Remove(categoryThumbFile(id)); err != nil && !os.IsNotExist(err) {
		log.Printf("[categories] remove superseded upload for %s: %v", id, err)
	}
}

// isHtmxDialogRequest reports whether r came from the record dialog's own
// hx-boosted forms (ut-docs#2020) rather than a plain browser submission.
// Local to this file rather than reused from httpx.IsFragmentSwap: that
// helper's own doc comment scopes it to GET-navigation fragment swaps
// (ut-docs#1950's /items rail); the check itself (HX-Request minus a
// history-restore, which a POST never carries anyway) is identical, but
// naming this separately keeps a POST-mutation reader from wondering why a
// GET-fragment-swap helper is here.
func isHtmxDialogRequest(r *http.Request) bool {
	return strings.EqualFold(r.Header.Get("HX-Request"), "true")
}

// redirectCategories answers a categories mutation's SUCCESS with a
// navigation back to GET /categories.
//
// Before ut-docs#2020, every mutation form was a plain `<form
// method="post">` with no hx-post/hx-boost, so a bare 303 caused a real
// full-page browser navigation and htmx never touched it (an earlier draft
// of THIS card once added an "HX-Request"-conditional HX-Redirect here in
// anticipation of the forms one day going htmx; independent review at the
// time found that reasoning backwards for a plain form and it was
// reverted). The forms actually are hx-boosted now — a bare 303 to an
// hx-boosted submit is followed *transparently by the browser's fetch/XHR
// layer*, and its final response body (the whole /categories page) would
// land wherever the form's hx-target points, not as a real navigation.
// HX-Redirect is htmx's own signal for "do a real browser navigation to
// this URL" instead: it bypasses swap logic entirely, restoring the exact
// pre-#2020 UX. A non-htmx caller (should not exist once the templates are
// converted, but kept as a safety net — same shape as
// renderCategoryDialogError's fallback below) gets the plain redirect.
func redirectCategories(w http.ResponseWriter, r *http.Request, target string) {
	if isHtmxDialogRequest(r) {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusOK)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// renderCategoryDialogError answers a REFUSED categories mutation. Before
// ut-docs#2020 every refusal redirected to /categories?err=... same as a
// success, and by the time the page re-rendered the dialog was gone —
// closed by the redirect — and whatever the operator had typed with it.
//
// The dialog's forms now hx-boost (ut-docs/reference/
// list-and-dialog-pattern.md), targeting "#category-dialog-msg" — the
// dialog's own aria-live region — with hx-swap="innerHTML", so this
// renders just the translated TEXT that goes inside it
// (record_dialog_msg.html), never the region element itself (an
// outerHTML swap would replace that node, and a live region generally
// must stay the same node across a content change for assistive tech to
// announce it). The dialog and the form the operator is typing in are
// never mentioned in this response at all, so an innerHTML swap of the
// message region cannot touch either. Answered as a non-2xx status
// specifically because app.js's global htmx:beforeSwap handler
// force-swaps a non-2xx response when it is real, non-empty text/html —
// and marks it NOT an error, so the page-wide #pos-alert-style banner does
// not also fire for the same failure (list-and-dialog-pattern.md's "one
// error, one place" rule / this card's AC4). A failure this can't answer
// at all — no response reached the browser, or a plain-text 403/500 that
// beforeSwap does not force-swap — is record-dialog.js's
// dialogFailureFallback's job instead, via the SAME element.
//
// A non-htmx caller (should not exist once the templates are converted,
// but a page route must still answer *something* sane to a direct POST —
// e.g. a bookmarked/curl'd request, or a future regression in the
// template) falls back to the pre-#2020 redirect-with-query-string shape,
// unchanged: this is exactly what every existing Go-level test in this
// file (which posts with no HX-Request header) already exercises.
func renderCategoryDialogError(w http.ResponseWriter, r *http.Request, errKey string, errCount int) {
	if !isHtmxDialogRequest(r) {
		// &count= only when it's the one key that reads it (the %d
		// placeholder in categories.error.deactivate_blocked) — matches the
		// exact pre-#2020 query shape byte for byte, which the existing
		// Go-level tests in this file already pin.
		target := "/categories?err=" + errKey
		if errKey == "categories.error.deactivate_blocked" {
			target = fmt.Sprintf("%s&count=%d", target, errCount)
		}
		redirectCategories(w, r, target)
		return
	}
	msg := httpx.T(httpx.RequestLocale(r), errKey)
	if errKey == "categories.error.deactivate_blocked" {
		msg = fmt.Sprintf(msg, errCount)
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusBadRequest)
	httpx.RenderPartial("ui/partials/record_dialog_msg.html", map[string]any{"msg": msg})(w, r)
}

// registerCategories wires the categories admin page (ut-docs#1898):
// create/rename/deactivate/reorder for the flat category list. Categories
// exist in the schema and are read in several places (the sale-screen tab
// bar, the catalog item-edit <select>, kitchen-station routing) but had no
// management screen anywhere — this is that screen. Manager/admin only,
// modelled most closely on tables_page.go (which has both gating variants
// this needs: a bare-body 403 for the API/fragment routes, a full RenderError
// page for the page route itself, ut-docs#1455). Flat list only — no
// nested/tree UI, no parent_id editing (out of scope, see the card's
// non-goals); move-up/move-down reorder buttons, not drag-and-drop
// (ut-docs#1221, same reasoning as buttons_admin.html's reorder grid).
// Categories are soft-disabled, never deleted from this UI (hard delete
// stays reachable only through the primary-side sync-prune path).
func registerCategories(mux *http.ServeMux, d *common.Deps) {
	catRepo := data.NewCatalogRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)
	modRepo := data.NewModifierRepo(d.Db)

	// requireManager gates on the "settings" action, same as
	// tables_page.go/kitchen_stations_page.go — no narrower catalog-admin
	// action exists yet, and adding one would need its own seed-data
	// migration (out of scope for this card).
	requireManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "settings") {
			common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	// requirePageManager is requireManager's gate for the /categories page
	// route itself, answered as a full RenderError page instead of a bare
	// LocalizedError body (ut-docs#1455: a page route must not fall back to
	// a bare, rail-less 403). requireManager stays as-is for the
	// /api/categories/* mutation routes below — a bare, plain-text 403
	// LocalizedError body, which the dialog's own forms being hx-boosted
	// now (ut-docs#2020) doesn't change: app.js's htmx:beforeSwap only
	// force-swaps a text/html body, so this one instead reaches
	// record-dialog.js's dialogFailureFallback and surfaces as the
	// generic "something went wrong" dialog message, not the specific
	// translated reason renderCategoryDialogError's own responses carry.
	requirePageManager := func(w http.ResponseWriter, r *http.Request) (auth.User, bool) {
		if !canPerform(d, r, "settings") {
			httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
			return auth.User{}, false
		}
		u, _ := auth.FromContext(r.Context())
		return u, true
	}

	// requirePrimary gates the dialog's own mutations (create/rename/active,
	// hx-boosted since ut-docs#2020 — not plain forms any more) on this
	// till being the primary (ut-docs#1585 family): categories syncs
	// shop-wide as an admin table (adminTables, sync_admin_repo.go) via a
	// one-way primary-wins pull, so a write accepted on a satellite would
	// silently vanish on the very next admin pull -- refuse it up front
	// instead, same pattern as tables_page.go/locations_page.go's
	// requirePrimary.
	requirePrimary := func(w http.ResponseWriter, r *http.Request) bool {
		if d.SyncPrimaryURL(r.Context()) != "" {
			renderCategoryDialogError(w, r, "categories.error.replica_use_primary", 0)
			return false
		}
		return true
	}

	// requirePrimaryFetch is requirePrimary's counterpart for the reorder
	// route below, which is JS fetch-driven (categories.html's own script),
	// not a plain form post: a redirect there would just be followed
	// silently by fetch and read back as a 200 "success", hiding the refusal
	// from the reorder JS's res.ok check. Answers with a status + localized
	// body instead, same shape as buttons_api.go's own reorder endpoint gate
	// (409, so the client can tell "you're on a replica" apart from a
	// generic failure).
	requirePrimaryFetch := func(w http.ResponseWriter, r *http.Request) bool {
		if d.SyncPrimaryURL(r.Context()) != "" {
			common.LocalizedError(w, r, http.StatusConflict, "categories.error.replica_use_primary")
			return false
		}
		return true
	}

	audit := func(r *http.Request, actorID, targetID, action string) {
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "category", targetID, action, nil, now, "")
	}
	// auditImage is audit plus the image outcome (ut-docs#2500) — "icon:
	// <key>", "upload", "none" or "" (unchanged) — so the log records what
	// a Save did to the category's picture, not just that it happened.
	auditImage := func(r *http.Request, actorID, targetID, action, image string) {
		now := time.Now().UTC().Format(time.RFC3339)
		var payload any
		if image != "" {
			payload = map[string]any{"image": image}
		}
		_ = posRepo.InsertAudit(r.Context(), nil, actorID, "category", targetID, action, payload, now, "")
	}

	// categoryRow is one list row: the admin read plus the comma-joined
	// modifier-group and kitchen-station ids the row's data-groups/
	// data-stations attributes hand categories.html's own script for
	// ticking the dialog's checkboxes on open (ut-docs#2284). Joined here,
	// not in the template, so the attribute is one plain string.
	type categoryRow struct {
		data.CategoryAdminRow
		Groups   string
		Stations string
	}

	renderCategories := func(w http.ResponseWriter, r *http.Request, errKey string, errCount int) {
		ctx := r.Context()
		rows, err := catRepo.ListCategoriesForAdmin(ctx)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		// ut-docs#2284: the dialog's three pickers. Groups and stations are
		// each ONE shop-wide query plus ONE all-categories link query (not
		// a per-row lookup — an N+1 over the whole category list on every
		// render), same shape kitchen_stations_page.go's renderPage uses.
		groups, err := modRepo.ListActiveModifierGroups(ctx)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		stations, err := posRepo.ListKitchenStations(ctx)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		groupLinks, err := modRepo.AllCategoryModifierGroupLinks(ctx)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		stationRoutes, err := posRepo.AllCategoryStationRoutes(ctx)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "common.error.server", err)
			return
		}
		catRows := make([]categoryRow, 0, len(rows))
		for _, c := range rows {
			catRows = append(catRows, categoryRow{
				CategoryAdminRow: c,
				Groups:           strings.Join(groupLinks[c.ID], ","),
				Stations:         strings.Join(stationRoutes[c.ID], ","),
			})
		}
		categoriesData := map[string]any{
			"title":      httpx.T(httpx.RequestLocale(r), "page.title.categories"),
			"theme":      d.CurrentState().Theme,
			"menuItems":  d.MenuSnapshot(),
			"categories": catRows,
			"groups":     groups,
			"stations":   stations,
			"itemColors": catalogtypes.ItemColors(),
			// ut-docs#2500: the dialog's image picker offers the SAME
			// built-in set as the item editor, grouped (ut-docs#2506).
			"builtinIconGroups": catimport.BuiltinIconGroups(),
			"errKey":            errKey,
			"errCount":          errCount,
		}
		// ut-docs#1950: /categories is one of the /items rail's five section
		// destinations — an htmx request from that panel (NOT a stale history
		// restore, see httpx.IsFragmentSwap) gets just the "content" block
		// plus an out-of-band refresh of the rail so its is-current highlight
		// follows the click; a plain browser GET (deep link, or the redirect
		// a mutation falls back to) still gets the exact same full standalone
		// page as before this card.
		if httpx.IsFragmentSwap(w, r) {
			httpx.RenderContentFragment("ui/pages/categories.html", categoriesData)(w, r)
			itemsnav.WriteRailOOB(w, r, httpx.FuncsFor(httpx.RequestLocale(r)), "/categories", itemsnav.Resolve(httpx.RequestLocale(r), d.ItemsAmendmentsSnapshot()))
			return
		}
		httpx.Render("ui/pages/categories.html", categoriesData)(w, r)
	}

	mux.HandleFunc("GET /categories", func(w http.ResponseWriter, r *http.Request) {
		if _, ok := requirePageManager(w, r); !ok {
			return
		}
		// count only matters alongside categories.error.deactivate_blocked
		// (the %d placeholder it interpolates); absent/unparseable elsewhere
		// just renders as 0, which the template never reads for any other key.
		count, _ := strconv.Atoi(r.URL.Query().Get("count"))
		renderCategories(w, r, httpx.QueryErrKey(r), count)
	})

	// categoryForm is the dialog's submitted state (ut-docs#2284): name and
	// colour on the row itself, plus the two link sets one Save replaces
	// wholesale (SetCategoryModifierGroups / SetCategoryStationRoutes —
	// "every ticked id", so an unticked box is a removal, same as
	// kitchen_stations_page.go's setRoutes).
	type categoryForm struct {
		name       string
		color      string
		groupIDs   []string
		stationIDs []string
		// ut-docs#2500: the image choice. icon is "" (keep whatever is
		// there), "none" (clear) or a built-in key already resolved to
		// iconPath. photo is a decoded, downscaled upload — validated
		// here, BEFORE any row is written, and written to disk only after
		// the row is saved. A photo wins over an icon key.
		icon     string
		iconPath string
		photo    image.Image
		// hidden (manage-shop catalog contract §7.2(5)) is the "Show on
		// the sale screen" box, inverted: nil when the post carried no
		// show_on_sale_screen_field marker (an older page), so an absent
		// checkbox is never read as "hide".
		hidden *bool
	}

	// parseCategoryForm reads and validates the dialog's form. The colour
	// is checked against the SAME fixed palette the item editor uses
	// (catalogtypes.ValidItemColor, see ItemColors' own doc comment on why
	// that allowlist is a real security control: the value flows into a
	// CSS custom property on the sale screen's category tab). Group and
	// station ids are checked against what actually exists — a stale
	// dialog or a tampered form must not be able to link a deactivated
	// group or an unknown station — and refused as a whole so nothing
	// half-saves.
	parseCategoryForm := func(r *http.Request) (categoryForm, string) {
		// ut-docs#2500: the dialog form is multipart (it carries the image
		// file). ParseForm alone silently ignores a multipart body, so
		// every field — group_id/station_id included — would arrive empty
		// (the ut-docs#2018 trap). A urlencoded post (an older page, a
		// test, a curl) still parses the plain way.
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			if !errors.Is(err, http.ErrNotMultipart) {
				return categoryForm{}, "common.error.invalid_upload"
			}
			_ = r.ParseForm()
		}
		f := categoryForm{
			name:  strings.TrimSpace(r.PostFormValue("name")),
			color: strings.TrimSpace(r.PostFormValue("color")),
		}
		if f.name == "" {
			return f, "categories.error.name_required"
		}
		if !catalogtypes.ValidItemColor(f.color) {
			return f, "categories.error.color_invalid"
		}
		if r.PostFormValue("show_on_sale_screen_field") == "1" {
			h := r.PostFormValue("show_on_sale_screen") != "1"
			f.hidden = &h
		}
		f.icon = strings.TrimSpace(r.PostFormValue("icon"))
		if f.icon != "" && f.icon != "none" {
			p, ok := catimport.IconPath(f.icon)
			if !ok {
				return f, "categories.error.image_icon_invalid"
			}
			f.iconPath = p
		}
		if r.MultipartForm != nil {
			if file, hdr, err := r.FormFile("image"); err == nil {
				defer file.Close()
				if hdr.Size > 10<<20 {
					// Review finding: a 10–11 MB photo would be silently
					// truncated by the LimitReader below and reported as
					// "not a valid image"; say "too large" instead.
					return f, "catalog.error.image_too_large"
				}
				if hdr.Size > 0 {
					raw, readErr := io.ReadAll(io.LimitReader(file, 10<<20))
					if readErr != nil {
						return f, "catalog.error.image_invalid"
					}
					img, err := imaging.PrepareThumb(raw)
					if err != nil {
						return f, catalog.ThumbErrorKey(err)
					}
					f.photo = img
				}
			}
		}
		for _, id := range r.PostForm["group_id"] {
			if id = strings.TrimSpace(id); id != "" {
				f.groupIDs = append(f.groupIDs, id)
			}
		}
		for _, id := range r.PostForm["station_id"] {
			if id = strings.TrimSpace(id); id != "" {
				f.stationIDs = append(f.stationIDs, id)
			}
		}
		if len(f.groupIDs) > 0 {
			groups, err := modRepo.ListActiveModifierGroups(r.Context())
			if err != nil {
				return f, "categories.error.update"
			}
			known := make(map[string]bool, len(groups))
			for _, g := range groups {
				known[g.ID] = true
			}
			for _, id := range f.groupIDs {
				if !known[id] {
					return f, "categories.error.invalid_selection"
				}
			}
		}
		if len(f.stationIDs) > 0 {
			stations, err := posRepo.ListKitchenStations(r.Context())
			if err != nil {
				return f, "categories.error.update"
			}
			known := make(map[string]bool, len(stations))
			for _, s := range stations {
				known[s.ID] = true
			}
			for _, id := range f.stationIDs {
				if !known[id] {
					return f, "categories.error.invalid_selection"
				}
			}
		}
		return f, ""
	}

	// saveCategoryLinks writes the two link sets after the row itself is
	// saved. Both tables are adminTables (sync_admin_repo.go) with their
	// own sync-version triggers (023/031), so a satellite picks either
	// change up on its next admin pull exactly like an item-level link.
	// Reports false after answering the request itself.
	saveCategoryLinks := func(w http.ResponseWriter, r *http.Request, id string, f categoryForm) bool {
		// Review finding (ut-docs#2284): the group picker lists ACTIVE
		// groups only, so a link to a group deactivated AFTER it was linked
		// has no checkbox in the dialog at all — an untickable box is not
		// an unticked one. Without this, the very next unrelated Save (a
		// rename) would replace-all the links and silently drop it, and
		// reactivating the group would NOT bring the inheritance back.
		// Existing inactive links are therefore carried through the
		// replace-all write, appended after the ticked ones (the picker
		// can't express their order either). Stations need no equivalent:
		// that picker lists disabled stations too, labelled, so their tick
		// survives on its own.
		groupIDs := f.groupIDs
		if linked, err := modRepo.ListAllGroupsForCategory(r.Context(), id); err == nil {
			submitted := make(map[string]bool, len(groupIDs))
			for _, gid := range groupIDs {
				submitted[gid] = true
			}
			for _, g := range linked {
				if !g.IsActive && !submitted[g.ID] {
					groupIDs = append(groupIDs, g.ID)
				}
			}
		} else {
			renderCategoryDialogError(w, r, "categories.error.update", 0)
			return false
		}
		if err := modRepo.SetCategoryModifierGroups(r.Context(), id, groupIDs); err != nil {
			renderCategoryDialogError(w, r, "categories.error.update", 0)
			return false
		}
		if err := posRepo.SetCategoryStationRoutes(r.Context(), id, f.stationIDs); err != nil {
			renderCategoryDialogError(w, r, "categories.error.update", 0)
			return false
		}
		return true
	}

	// saveCategoryImage applies the dialog's image choice after the row
	// itself is saved (ut-docs#2500). An upload and a built-in icon are
	// mutually exclusive, same as an item's: writing one removes the
	// other's file. Returns the audit label ("" = unchanged) and false
	// after answering the request itself.
	saveCategoryImage := func(w http.ResponseWriter, r *http.Request, id string, f categoryForm) (string, bool) {
		switch {
		case f.photo != nil:
			if err := imaging.WriteThumbPNG(f.photo, categoryThumbFile(id)); err != nil {
				log.Printf("[categories] write thumb for %s: %v", id, err)
				renderCategoryDialogError(w, r, "categories.error.update", 0)
				return "", false
			}
			if err := catRepo.SetCategoryImage(r.Context(), id, categoryThumbURL(id)); err != nil {
				renderCategoryDialogError(w, r, "categories.error.update", 0)
				return "", false
			}
			return "upload", true
		case f.icon == "none":
			if err := catRepo.SetCategoryImage(r.Context(), id, ""); err != nil {
				renderCategoryDialogError(w, r, "categories.error.update", 0)
				return "", false
			}
			removeCategoryUpload(id)
			return "none", true
		case f.iconPath != "":
			if err := catRepo.SetCategoryImage(r.Context(), id, f.iconPath); err != nil {
				renderCategoryDialogError(w, r, "categories.error.update", 0)
				return "", false
			}
			removeCategoryUpload(id)
			return "icon:" + f.icon, true
		}
		return "", true
	}

	mux.HandleFunc("POST /api/categories", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		if !requirePrimary(w, r) {
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, categoryUploadMaxBytes)
		f, errKey := parseCategoryForm(r)
		if errKey != "" {
			renderCategoryDialogError(w, r, errKey, 0)
			return
		}
		// The hidden flag rides in the create's own INSERT (review finding
		// 6): nothing half-saved if it fails.
		id, err := catRepo.CreateCategoryWithColorHidden(r.Context(), f.name, f.color, f.hidden != nil && *f.hidden)
		if err != nil {
			key := "categories.error.create"
			if err == data.ErrCategoryNameRequired {
				key = "categories.error.name_required"
			}
			renderCategoryDialogError(w, r, key, 0)
			return
		}
		if !saveCategoryLinks(w, r, id, f) {
			return
		}
		imageAudit, ok := saveCategoryImage(w, r, id, f)
		if !ok {
			return
		}
		auditImage(r, actor.ID, id, "category_create", imageAudit)
		redirectCategories(w, r, "/categories")
	})

	mux.HandleFunc("POST /api/categories/{id}", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		if !requirePrimary(w, r) {
			return
		}
		id := r.PathValue("id")
		// ut-docs#2500: id now reaches a filesystem path (the uploaded
		// photo), so refuse a traversal-shaped one before anything else.
		if !safeCategoryID(id) {
			renderCategoryDialogError(w, r, "categories.error.not_found", 0)
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, categoryUploadMaxBytes)
		f, errKey := parseCategoryForm(r)
		if errKey != "" {
			renderCategoryDialogError(w, r, errKey, 0)
			return
		}
		// The hidden flag rides in the same UPDATE (review finding 6).
		if err := catRepo.UpdateCategoryWithHidden(r.Context(), id, f.name, f.color, f.hidden); err != nil {
			key := "categories.error.rename"
			switch err {
			case data.ErrCategoryNameRequired:
				key = "categories.error.name_required"
			case data.ErrCategoryNotFound:
				key = "categories.error.not_found"
			}
			renderCategoryDialogError(w, r, key, 0)
			return
		}
		if !saveCategoryLinks(w, r, id, f) {
			return
		}
		imageAudit, ok := saveCategoryImage(w, r, id, f)
		if !ok {
			return
		}
		// "category_update", not "category_rename" — this same handler now
		// also writes colour, the modifier-group/kitchen-station links
		// (ut-docs#2284) and the image (ut-docs#2500), not just the name.
		auditImage(r, actor.ID, id, "category_update", imageAudit)
		redirectCategories(w, r, "/categories")
	})

	mux.HandleFunc("POST /api/categories/{id}/active", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		if !requirePrimary(w, r) {
			return
		}
		id := r.PathValue("id")
		_ = r.ParseForm()
		enable := r.PostFormValue("active") == "1"
		if err := catRepo.SetCategoryActive(r.Context(), id, enable); err != nil {
			// The blocked-deactivate case carries the active item count so
			// renderCategoryDialogError can interpolate it into
			// categories.error.deactivate_blocked's %d placeholder (same
			// printf convention tables.html's tables.status.open_minutes
			// already uses) — the operator sees WHY the deactivate was
			// refused, not just "failed", and no row is changed.
			if hasItems, ok := err.(*data.ErrCategoryHasItems); ok {
				renderCategoryDialogError(w, r, "categories.error.deactivate_blocked", hasItems.Count)
				return
			}
			key := "categories.error.update"
			if err == data.ErrCategoryNotFound {
				key = "categories.error.not_found"
			}
			renderCategoryDialogError(w, r, key, 0)
			return
		}
		action := "category_deactivate"
		if enable {
			action = "category_activate"
		}
		audit(r, actor.ID, id, action)
		redirectCategories(w, r, "/categories")
	})

	// Reorder from move-up/move-down buttons (ut-docs#1221's pattern, see
	// categories.html's own script) — mirrors buttons_api.go's
	// POST /api/buttons/reorder body shape: repeated "ids" form values in
	// display order, or a single comma-joined field as a fallback.
	mux.HandleFunc("POST /api/categories/reorder", func(w http.ResponseWriter, r *http.Request) {
		actor, ok := requireManager(w, r)
		if !ok {
			return
		}
		if !requirePrimaryFetch(w, r) {
			return
		}
		// categories.html's reorder script posts FormData (multipart) —
		// ParseForm alone ignores multipart bodies, so every real-browser
		// reorder answered `400 ids required` while the urlencoded test
		// passed (ut-docs#2018). Same guard as buttons_api.go's reorder.
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			_ = r.ParseMultipartForm(1 << 20)
		} else {
			_ = r.ParseForm()
		}
		ids := r.Form["ids"]
		if len(ids) == 1 && strings.Contains(ids[0], ",") {
			ids = strings.Split(ids[0], ",")
		}
		if len(ids) == 0 {
			http.Error(w, "ids required", http.StatusBadRequest)
			return
		}
		for i := range ids {
			ids[i] = strings.TrimSpace(ids[i])
		}
		if err := catRepo.SetCategorySortOrder(r.Context(), ids); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "categories.error.update", "categories_reorder", err)
			return
		}
		audit(r, actor.ID, strings.Join(ids, ","), "category_reorder")
		w.WriteHeader(http.StatusNoContent)
	})
}
