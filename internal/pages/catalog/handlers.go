package catalog

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"image/png"
	"io"
	"log"
	"math"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/barcode"
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/catimport"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/imaging"
	productlookup "github.com/universaltill/universal-till/internal/lookup"
	"github.com/universaltill/universal-till/internal/pages/common"
	"github.com/universaltill/universal-till/internal/pages/itemsnav"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/pos"
)

// modifierAdminItem is the template data shape modifier_group_admin.html
// (ut-docs#1957) expects for one item's worth of modifier-group assignment:
// its groups (active or not — ListAllGroupsForItem's shape) plus Target,
// the container id (without a leading #) every one of the rendered forms'
// hx-target/hx-swap points back at. Since ADR-0101 (ut-docs#2399) this
// backs ONLY the item-scoped nested dialog (attach-only); the shop-wide
// /modifiers page renders data.ModifierGroupAdmin rows of its own (see
// renderModifiersList) and no longer groups anything per item.
type modifierAdminItem struct {
	ItemID         string
	ModifierGroups []data.ModifierGroup
	// AttachableGroups is this item's "attach an existing group" picker
	// options (ut-docs#2046): every other active shop group not already
	// linked to ItemID. Left nil when there is nothing left to attach.
	AttachableGroups []data.ModifierGroup
	// InheritedGroups is what ItemID inherits from its CATEGORY (ADR-0094,
	// ut-docs#2284): ListInheritedGroupsForItem's shape, OptedOut set per
	// group, so the item-scoped dialog can show each one greyed/labelled
	// and offer the skip / use-again toggle. Only the item-scoped panel
	// renders it (modifier_groups_item_panel); the shop-wide /modifiers
	// list leaves it nil.
	InheritedGroups []data.ModifierGroup
	Target          string
	// Notice is an already-translated, already-formatted message to show
	// inline above this fragment (ut-docs#2046, independent-review finding)
	// — e.g. the detach-guard's refusal. Plain http.Error/LocalizedError
	// answers text/plain, which htmx never swaps in (app.js's own
	// ut-docs#916 comment on this), so a refusal handled that way is a
	// silent no-op on every admin page without a #pos-alert fallback. This
	// field is instead rendered INSIDE the normal HTML fragment the mutation
	// already re-renders, so the existing swap carries the message for free.
	// Empty on every ordinary (non-error) render.
	Notice string
}

// modifierCard is one group's card on /modifiers (ADR-0101 §3,
// ut-docs#2399): the shop-wide admin read (group + every option + linked
// categories/items) plus CategoryOptions — EVERY shop category with a
// Linked flag, the checkbox row the template renders. Built once per page
// in modifiersPageData from the four-query repo read, so the template
// never has to test membership itself (Go templates have no "in").
type modifierCard struct {
	data.ModifierGroupAdmin
	CategoryOptions []modifierCategoryOption
}

// modifierCategoryOption is one checkbox in a card's "Assigned to →
// Categories" row. IsActive is the category's own flag: a retired category
// that still links the group stays visible (greyed) so the merchant can
// see and clear the link rather than have it silently disappear.
type modifierCategoryOption struct {
	ID       string
	Name     string
	IsActive bool
	Linked   bool
}

// newLookupClient is a test seam: production resolves barcodes against the
// real Open*Facts product databases; tests swap in a client pointed at
// hermetic httptest servers so nothing in this package can ever touch the
// network under test.
var newLookupClient = func() *productlookup.Client { return productlookup.NewClient(nil, nil) }

// Register mounts catalog list/create and barcode attach endpoints.
// func Register(mux *http.ServeMux, db *sql.DB, theme string, menu []map[string]string) {
func Register(mux *http.ServeMux, d *common.Deps) {
	repo := data.NewCatalogRepo(d.Db)
	posRepo := data.NewPOSRepo(d.Db)
	modRepo := data.NewModifierRepo(d.Db)
	lookupClient := newLookupClient()

	// requirePrimary gates catalog mutation on this till being the primary
	// (ut-docs#1667, extended by ut-docs#1689): item_modifier_groups/
	// item_modifier_options/items/item_variants/item_barcodes/
	// variant_barcodes are all synced shop-wide (sync_admin_repo.go's
	// adminTables) via a one-way primary-wins pull, so a write accepted on a
	// satellite would silently vanish (a new row deleted, an edit reverted)
	// on the very next admin pull — refuse it up front instead, same pattern
	// as registers_page.go's requirePrimary. Unlike that page's full-page
	// redirect, these handlers return an HTMX fragment, so the refusal is a
	// plain localized error response like this file's own validation
	// branches (e.g. catalog.error.invalid_request) rather than a redirect.
	// The message key is a parameter (not hardcoded) because
	// "manage customization options" only reads correctly for the modifier
	// group/option routes below — the item/variant/barcode routes ut-docs#1689
	// added use their own, more general "manage the catalog" key instead of
	// silently reusing wording that doesn't fit them.
	requirePrimary := func(w http.ResponseWriter, r *http.Request, key string) bool {
		if d.SyncPrimaryURL(r.Context()) != "" {
			common.LocalizedError(w, r, http.StatusConflict, key)
			return false
		}
		return true
	}

	// requireCatalogManagement gates every mutating route below on the
	// "catalog_management" role_permissions action (ut-docs#2312): before
	// this, none of items/variants/modifier-groups/option-sets/barcodes
	// carried ANY permission check — reachable and mutable by every
	// signed-in operator, cashiers included. Same shape and same
	// UT_AUTH=off / fail-closed-on-no-session/DB-error behaviour as
	// internal/pages.canPerform, and the same denial key/status
	// tax_codes_page.go's requireManager ("tax_code_management") and
	// locations_page.go's requireManager ("stock_location_management")
	// already use for their own dedicated "management" actions — but
	// written out locally rather than calling pages.canPerform directly:
	// package pages already imports THIS package (catalog.Register, via
	// internal/pages/init.go) to mount these routes, so pages -> catalog
	// -> pages would be an import cycle. Checked BEFORE requirePrimary
	// (permission is more fundamental than till topology — a request that
	// fails both should hear "you can't do this" before "and this till
	// can't do it right now"), and as the very first line in the three
	// item/variant-image and icon handlers below that have no
	// requirePrimary call to anchor before (item_images is deliberately
	// excluded from the sync bundle — see their own doc comments).
	// catalogManagementAllowed is the bare boolean check both gate variants
	// below share, so a full-page GET handler and an HTMX/API mutation
	// handler read the identical auth decision from one place.
	catalogManagementAllowed := func(r *http.Request) bool {
		if auth.Disabled(os.Getenv("UT_AUTH")) {
			return true
		}
		u, ok := auth.FromContext(r.Context())
		if !ok {
			return false
		}
		can, err := d.AuthSvc.Can(r.Context(), u, "catalog_management")
		return err == nil && can
	}

	requireCatalogManagement := func(w http.ResponseWriter, r *http.Request) bool {
		if catalogManagementAllowed(r) {
			return true
		}
		common.LocalizedError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required")
		return false
	}

	// requireCatalogManagementPage is requireCatalogManagement's counterpart
	// for the three full-page GET routes below (/catalog, /modifiers,
	// /catalog/option-sets, ut-docs#2357): a page route must render the
	// shell's own themed 403 via httpx.RenderError, same as
	// tax_codes_page.go/locations_page.go gate their own GET handler —
	// LocalizedError's bare http.Error body is for the HTMX/API mutation
	// routes above, not a full document load.
	requireCatalogManagementPage := func(w http.ResponseWriter, r *http.Request) bool {
		if catalogManagementAllowed(r) {
			return true
		}
		httpx.RenderError(w, r, http.StatusForbidden, "common.error.manager_or_admin_required", nil)
		return false
	}

	writeJSON := func(w http.ResponseWriter, status int, data any, errMsg string) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		var errField any
		if errMsg != "" {
			errField = errMsg
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": data, "error": errField})
	}

	// Barcode → open product databases; pre-fills the new-item form
	// (G15 increment 1). Back-office convenience only, fails soft offline.
	mux.HandleFunc("GET /api/catalog/lookup", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagement(w, r) {
			return
		}
		barcode := strings.TrimSpace(r.URL.Query().Get("barcode"))
		if !productlookup.ValidBarcode(barcode) {
			writeJSON(w, http.StatusBadRequest, nil, "barcode must be 6-14 digits")
			return
		}
		product, err := lookupClient.Lookup(r.Context(), barcode)
		now := time.Now().UTC().Format(time.RFC3339)
		_ = posRepo.InsertAudit(r.Context(), nil, auth.UserID(r), "catalog", barcode, "barcode_lookup",
			map[string]any{"found": err == nil, "source": product.Source}, now, "")
		switch {
		case errors.Is(err, productlookup.ErrNotFound):
			writeJSON(w, http.StatusNotFound, nil, "barcode not found in product databases")
		case err != nil:
			writeJSON(w, http.StatusBadGateway, nil, "product databases unreachable")
		default:
			writeJSON(w, http.StatusOK, product, "")
		}
	})

	// Every mutation endpoint answers with row-level out-of-band fragments
	// for the ONE item it touched (ut-docs#1363) — see row_oob.go. The
	// request's primary swap is none, so these fragments ARE the UI update.
	// The mutation itself has already committed by the time this runs, so a
	// render/query failure here is logged rather than surfaced as an error
	// status the client would misread as "the save failed".
	writeRowOOB := func(w http.ResponseWriter, r *http.Request, itemID string, insert bool) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		if err := writeCatalogRowOOB(w, r, repo, funcs, itemID, insert); err != nil {
			log.Printf("[catalog] row oob for item %s: %v", itemID, err)
		}
	}

	// renderVariantsPanel answers with the per-item variants/barcodes editor.
	// Panel mutations also change what the items table shows (its variant and
	// barcode summaries), so the affected item's ROW rides along as an HTMX
	// out-of-band fragment when withTable is set (ut-docs#1363 — previously
	// this injected the entire re-rendered table).
	//
	// extra is merged into the panel's template data on top of the standard
	// fields — the option-set generator's one-shot confirmation line
	// (ut-docs#1900: "N variant(s) created" / "apply a set first") rides
	// along this way rather than through a second render path.
	renderVariantsPanelWith := func(w http.ResponseWriter, r *http.Request, itemID string, withTable bool, extra map[string]any) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		pdata := map[string]any{"ItemID": "", "ItemName": ""}
		if itemID != "" {
			if label, ok, err := repo.GetItemLabel(r.Context(), itemID); err == nil && ok {
				variants, _ := repo.VariantsForItem(r.Context(), itemID)
				itemBCs, _ := repo.BarcodesForItem(r.Context(), itemID)
				cost, _ := repo.ItemCostPrice(r.Context(), itemID)
				costMajor := ""
				if cost > 0 {
					// Decimal-aware, same as the modifier-option price
					// handling below: a 0-decimal currency (IRR/JPY/…)
					// renders whole units, never a hardcoded /100.
					decimals := httpx.CurrencyByCode(d.CurrentState().Currency).Decimals
					costMajor = strconv.FormatFloat(float64(cost)/math.Pow(10, float64(decimals)), 'f', decimals, 64)
				}
				leadTimeDays, _ := repo.ItemLeadTimeDays(r.Context(), itemID)
				reorderLevel, _ := repo.ItemReorderLevel(r.Context(), itemID)
				// ADR-0020: shows deactivated groups/options too (unlike the
				// sale-time ListGroupsForItem) so a manager can reactivate one.
				modGroups, _ := data.NewModifierRepo(d.Db).ListAllGroupsForItem(r.Context(), itemID)
				// ut-docs#1957: this panel only shows a compact, read-only
				// summary of the item's ACTIVE group names now — the CRUD
				// itself moved to /modifiers and the nested "Manage
				// customization groups" dialog (renderItemModifierGroupsPanel
				// below). A deactivated group is deliberately left out of
				// this summary (it isn't offered at sale time either); it's
				// still reachable for reactivation from the two admin
				// surfaces above, both driven by modGroups' full ListAll
				// fetch, unchanged.
				var activeModGroupNames []string
				for _, g := range modGroups {
					if g.IsActive {
						activeModGroupNames = append(activeModGroupNames, g.Name)
					}
				}
				// ut-docs#2284: the groups the item inherits from its category
				// (ADR-0094), minus its opt-outs and minus any it also links
				// directly (the summary names that one once, as its own) —
				// so an item whose customization comes entirely from its
				// category never reads "no customization groups yet".
				ownIDs := make(map[string]bool, len(modGroups))
				for _, g := range modGroups {
					ownIDs[g.ID] = true
				}
				inheritedGroups, _ := data.NewModifierRepo(d.Db).ListInheritedGroupsForItem(r.Context(), itemID)
				var inheritedNames []string
				for _, g := range inheritedGroups {
					if !g.OptedOut && !ownIDs[g.ID] {
						inheritedNames = append(inheritedNames, g.Name)
					}
				}
				// ut-docs#2284: kitchen-printer routing from the item's side —
				// what its category routes to, and the item's own override
				// (item_station_routes, which ResolveKitchenStations lets win
				// outright). Best-effort like every other read here: a shop
				// with no stations (or a fixture without the tables) simply
				// renders no routing section.
				stationChecks, categoryStationNames, hasItemRoutes := itemRoutingData(r.Context(), repo, posRepo, itemID)
				// ut-docs#1900: the shop's option sets (active only — the
				// checkbox row offers what can be applied now) and which of
				// them this item's range is generated from.
				optRepo := data.NewOptionSetRepo(d.Db)
				allSets, _ := optRepo.ListOptionSets(r.Context())
				optionSets := make([]data.OptionSetView, 0, len(allSets))
				for _, s := range allSets {
					if s.IsActive {
						optionSets = append(optionSets, s)
					}
				}
				applied, _ := optRepo.ItemOptionSets(r.Context(), itemID)
				appliedIDs := make(map[string]bool, len(applied))
				for _, s := range applied {
					appliedIDs[s.ID] = true
				}
				// ut-docs#1324: the item's own thumbnail, resolved through
				// item_images (role=thumbnail) — same source of truth the
				// admin Catalog list (#1842), self-order kiosk (#1870) and
				// AI-identify (#1875) already use, instead of this panel's
				// old imgExists-on-disk convention check. Best-effort like
				// those callers: "" on error just means no image, same as a
				// genuinely missing row. The per-variant photo itself stays
				// disk-only (no variant_id column on item_images) — this
				// only covers the item-level thumbnail (panel header, and
				// the per-variant-row fallback when a variant has no photo
				// of its own).
				itemImg, _ := repo.ItemThumbnailFor(r.Context(), itemID)
				pdata = map[string]any{
					"ItemID":               itemID,
					"ItemName":             label.Name,
					"ItemImageURL":         itemImg,
					"Variants":             variants,
					"ItemBarcodes":         itemBCs,
					"CostMajor":            costMajor,
					"LeadTimeDays":         leadTimeDays,
					"ReorderLevel":         reorderLevel,
					"ModifierGroups":       modGroups,
					"ModifierGroupNames":   strings.Join(activeModGroupNames, ", "),
					"InheritedGroupNames":  strings.Join(inheritedNames, ", "),
					"Stations":             stationChecks,
					"CategoryStationNames": categoryStationNames,
					"HasItemRoutes":        hasItemRoutes,
					"OptionSets":           optionSets,
					"AppliedSetIDs":        appliedIDs,
				}
				for k, v := range extra {
					pdata[k] = v
				}
			}
		}
		httpx.RenderWith(files(
			filepath.Join("web", "ui", "partials", "catalog_variants.html"),
		), funcs)("catalog_variants", pdata)(w, r)
		if withTable {
			writeRowOOB(w, r, itemID, false)
		}
	}
	renderVariantsPanel := func(w http.ResponseWriter, r *http.Request, itemID string, withTable bool) {
		renderVariantsPanelWith(w, r, itemID, withTable, nil)
	}

	// modifierGroupAdminFiles is the file set every modifier-group-admin
	// render below shares — the reusable modifier_group_admin.html partial
	// (ut-docs#1957) plus whichever wrapper template names its own
	// container ("modifiers_list" from modifiers.html, or
	// "modifier_groups_item_panel" from modifier_group_admin.html itself).
	modifierGroupAdminFiles := files(
		filepath.Join("web", "ui", "pages", "modifiers.html"),
		filepath.Join("web", "ui", "partials", "modifier_group_admin.html"),
	)

	// modifierItemPickerJSON (ut-docs#2330, e2e-caught regression): before
	// AttachOnly, the item-editor's own nested dialog was the ONLY place a
	// shop's very first modifier group — for ANY item, not just an
	// already-modifier-bearing one — ever got created (its "add new group"
	// form rendered unconditionally, regardless of whether ModifierGroups
	// was empty). Removing that form from the item-editor without giving
	// /modifiers an equivalent broke group creation entirely for any item
	// not already listed there — caught by
	// osk-decimal-sale-catalog-fields-1284.spec.ts's own two OSK-typing
	// tests, which create a fresh probe item and its first-ever group.
	// Since ADR-0101 (ut-docs#2399) the create form needs no item at all;
	// this picker now feeds each group card's own "Add item" search
	// (posting to /attach). This is the same id/name/sku picker shape as
	// inventory_page.go's own pickerItem/ItemsJSON (stock item picker) —
	// reused here rather than a new type, same JSON shape the
	// modifiers.html script expects.
	modifierItemPickerJSON := func(ctx context.Context) template.JS {
		type pickerItem struct {
			ID   string `json:"id"`
			Name string `json:"name"`
			SKU  string `json:"sku"`
		}
		items, err := repo.ListItems(ctx) // active items only — ListItems' own contract
		if err != nil {
			log.Printf("[catalog] modifiers item picker: %v", err)
			return template.JS("[]")
		}
		picker := make([]pickerItem, 0, len(items))
		for _, it := range items {
			picker = append(picker, pickerItem{ID: it.ID, Name: it.Name, SKU: it.SKU})
		}
		pickerJSON, _ := json.Marshal(picker)
		return template.JS(pickerJSON)
	}

	// modifiersPageData is the /modifiers screen's own data (ADR-0101 §3,
	// ut-docs#2399): every group in the shop exactly once, active or not,
	// assigned or not, each with its options and its category/item
	// assignments (ListAllModifierGroupsWithAssignments — four queries for
	// the whole page, never one per group), plus every category so each
	// card can render its "Assigned to" checkbox row (ListCategories —
	// inactive ones included, so a link to a retired category stays
	// visible and removable rather than silently disappearing), plus the
	// item picker JSON the per-card "Add item" search resolves against.
	modifiersPageData := func(ctx context.Context) (map[string]any, error) {
		groups, err := modRepo.ListAllModifierGroupsWithAssignments(ctx)
		if err != nil {
			return nil, err
		}
		categories, err := repo.ListCategories(ctx)
		if err != nil {
			return nil, err
		}
		cards := make([]modifierCard, 0, len(groups))
		// unassigned counts a group toward ut-docs#2406's bulk-delete
		// action using the SAME test modifiers.html already renders per
		// card (.modifier-unassigned's "neither Categories nor Items") —
		// never a second, independently-derived definition of unassigned.
		unassigned := 0
		for _, g := range groups {
			linked := make(map[string]bool, len(g.Categories))
			for _, c := range g.Categories {
				linked[c.ID] = true
			}
			card := modifierCard{ModifierGroupAdmin: g}
			// Every category, in the categories page's own order, so the
			// checkbox row reads the same on every card; a linked-but-
			// since-deleted category can't occur (the link row cascades
			// with the category), so no orphan handling is needed here.
			for _, c := range categories {
				card.CategoryOptions = append(card.CategoryOptions, modifierCategoryOption{
					ID: c.ID, Name: c.Name, IsActive: c.IsActive, Linked: linked[c.ID],
				})
			}
			if len(g.Categories) == 0 && len(g.Items) == 0 {
				unassigned++
			}
			cards = append(cards, card)
		}
		return map[string]any{
			"Groups":          cards,
			"ItemsJSON":       modifierItemPickerJSON(ctx),
			"UnassignedCount": unassigned,
		}, nil
	}

	// renderModifiersList answers with the /modifiers page's own shop-wide
	// re-render target (#modifiers-list, outerHTML swap) — one card per
	// group. Used both for the page's own initial load and for a mutation
	// whose originating form targets #modifiers-list (see
	// renderModifierMutationResult below).
	//
	// ut-docs#2090 review finding: deliberately NO "InItemsShell" key here.
	// A mutation POSTed from #modifiers-list always carries HX-Request:true
	// (it's an htmx form submit), so httpx.IsFragmentSwap(w, r) would read
	// true here even on a standalone, non-/items-shell /modifiers page —
	// the wrong signal, since this response never targets #items-panel,
	// only #modifiers-list. modifiers.html's own templates read
	// .InItemsShell on a bare map[string]any as nil -> false when the key
	// is absent, which is exactly the safe fallback (plain href="/items",
	// no hx- attributes) — don't "fix" that by adding the key here.
	renderModifiersList := func(w http.ResponseWriter, r *http.Request, notice string) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		pageData, err := modifiersPageData(r.Context())
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "modifiers.error.server", "catalog", err)
			return
		}
		pageData["Notice"] = notice
		httpx.RenderWith(modifierGroupAdminFiles, funcs)("modifiers_list", pageData)(w, r)
	}

	// renderItemModifierGroupsPanel answers with the nested "Manage
	// customization groups" dialog's own re-render target
	// (#modifier-groups-modal-list, outerHTML swap) — ONE item's groups,
	// active or not, same ListAllGroupsForItem shape the panel above has
	// always used. Used both by the dialog's own lazy-load GET (opened from
	// catalog_variants.html) and for a mutation whose originating form
	// targets #modifier-groups-modal-list.
	renderItemModifierGroupsPanel := func(w http.ResponseWriter, r *http.Request, itemID string, notice string) {
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		groups, err := modRepo.ListAllGroupsForItem(r.Context(), itemID)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "modifiers.error.server", "catalog", err)
			return
		}
		attachable, err := modRepo.ListAttachableModifierGroups(r.Context(), itemID)
		if err != nil {
			log.Printf("[catalog] attachable modifier groups for item %s: %v", itemID, err)
		}
		inherited, err := modRepo.ListInheritedGroupsForItem(r.Context(), itemID)
		if err != nil {
			log.Printf("[catalog] inherited modifier groups for item %s: %v", itemID, err)
		}
		// ut-docs#2284 review finding: a group both category-inherited AND
		// directly linked rendered twice — once under its own direct-link
		// section, once again under "from this item's category" with a
		// Skip button that reported success but changed nothing, because
		// ResolveGroupsForItem's direct link always wins regardless of the
		// category opt-out (see that resolver's own dedup). Filter it out
		// here exactly like the read-only summary above already does,
		// rather than offering a control with no effect.
		ownIDs := make(map[string]bool, len(groups))
		for _, g := range groups {
			ownIDs[g.ID] = true
		}
		visibleInherited := make([]data.ModifierGroup, 0, len(inherited))
		for _, g := range inherited {
			if !ownIDs[g.ID] {
				visibleInherited = append(visibleInherited, g)
			}
		}
		httpx.RenderWith(modifierGroupAdminFiles, funcs)("modifier_groups_item_panel", modifierAdminItem{
			ItemID:           itemID,
			ModifierGroups:   groups,
			AttachableGroups: attachable,
			InheritedGroups:  visibleInherited,
			Target:           "modifier-groups-modal-list",
			Notice:           notice,
		})(w, r)
	}

	// renderModifierMutationResult is the context-aware re-render dispatch
	// (ut-docs#1957) both /api/catalog/modifier-group and
	// /api/catalog/modifier-option end on: the create/update LOGIC (repo
	// calls, validation) is unchanged and shared either way — only WHICH
	// fragment answers the request varies, by reading back the exact same
	// Hx-Target header the request's own originating form set as its
	// hx-target (see modifier_group_admin.html's forms and this file's
	// modifierGroupAdminFiles-based renderers above). A request carrying
	// neither of the two new container ids — including every pre-existing
	// caller that predates this card, and any non-htmx caller — falls back
	// to the original #catalog-variants re-render, unchanged.
	//
	// status/notice (ut-docs#2046, independent-review finding): a refused
	// mutation (e.g. a stale attach picker's 409) must still surface to
	// the operator. A plain http.Error/LocalizedError body is
	// text/plain, which app.js's htmx:beforeSwap never force-swaps (see its
	// own ut-docs#916 comment) — outside the sale screen there is no
	// #pos-alert fallback either, so that refusal would be a silent no-op.
	// Answering through this SAME dispatch instead — text/html, the mutated
	// item's normal fragment, with Notice set — means the existing
	// hx-target/hx-swap on the very button that failed carries the message
	// for free, exactly like every ordinary successful mutation already
	// does. status/notice are the zero values (200, "") on every ordinary
	// success path, unchanged from before this card.
	renderModifierMutationResult := func(w http.ResponseWriter, r *http.Request, itemID string, status int, notice string) {
		if status == http.StatusOK {
			// ut-docs#2210: the sale screen fetches /ui/buttons exactly
			// once (index.html's hx-trigger="load"); a quick button's
			// routing between the customization picker and a straight
			// scan-and-add is baked into that one render and — with no
			// signal telling an already-open sale screen to refetch —
			// would otherwise never reflect a group created/updated
			// (including the Active toggle), attached, or detached here,
			// however live the underlying DB read already is. Every
			// successful mutation through this shared dispatch fires it;
			// a refused one (status != OK, e.g. a stale attach picker's
			// 409) changed nothing and must not. Same shape
			// as hold_api.go's "held-changed" / inventory_api.go's
			// "stock-updated" — buttons.html's swapped-in root listens for
			// it via hx-trigger="modifiers-changed from:body".
			w.Header().Set("HX-Trigger", "modifiers-changed")
		} else {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.WriteHeader(status)
		}
		switch strings.TrimSpace(r.Header.Get("Hx-Target")) {
		case "modifiers-list":
			renderModifiersList(w, r, notice)
		case "modifier-groups-modal-list":
			renderItemModifierGroupsPanel(w, r, itemID, notice)
		default:
			renderVariantsPanel(w, r, itemID, false)
		}
	}

	// The nested "Manage customization groups" dialog's own lazy-load GET
	// (ut-docs#1957) — opened from the compact summary button in
	// catalog_variants.html, kept as a real, bookmarkable-by-nothing GET
	// fragment endpoint rather than piggy-backing on item-variants, since
	// its container/shape is its own (#modifier-groups-modal-list, not
	// #catalog-variants).
	mux.HandleFunc("GET /api/catalog/modifier-groups-panel", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagement(w, r) {
			return
		}
		itemID := strings.TrimSpace(r.URL.Query().Get("item_id"))
		renderItemModifierGroupsPanel(w, r, itemID, "")
	})

	// Variant options as JSON — the labels form's variant picker.
	mux.HandleFunc("GET /api/catalog/variant-options", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagement(w, r) {
			return
		}
		itemID := strings.TrimSpace(r.URL.Query().Get("item_id"))
		variants, err := repo.VariantsForItem(r.Context(), itemID)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		type opt struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}
		opts := []opt{}
		for _, v := range variants {
			if v.IsActive {
				opts = append(opts, opt{ID: v.ID, Name: v.Name})
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"data": opts, "error": nil})
	})

	// Cost price (what the shop pays) — feeds the margin report. Accepts a
	// decimal in major units; stored as minor units (money boundary rule).
	mux.HandleFunc("POST /api/catalog/item-cost", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		itemID := strings.TrimSpace(r.Form.Get("panelItem"))
		raw := strings.TrimSpace(r.Form.Get("cost"))
		if itemID == "" {
			http.Error(w, "item required", http.StatusBadRequest)
			return
		}
		var minor int64
		if raw != "" {
			f, err := strconv.ParseFloat(raw, 64)
			// Upper bound is a sanity ceiling, not a real limit on shop
			// pricing (universaltill/ut-docs#276) — 1,000,000 major units
			// survives conversion to minor units at any known currency's
			// decimal count with no int64 overflow risk.
			if err != nil || f < 0 || f > 1_000_000 {
				http.Error(w, "invalid cost", http.StatusBadRequest)
				return
			}
			// Decimal-aware major→minor conversion (see the identical
			// reasoning on the modifier-option handler below): a hardcoded
			// *100 would store every cost 100x too high for a 0-decimal
			// currency shop and wreck the margin report.
			decimals := httpx.CurrencyByCode(d.CurrentState().Currency).Decimals
			minor = int64(math.Round(f * math.Pow(10, float64(decimals))))
		}
		if err := repo.SetItemCostPrice(r.Context(), itemID, minor); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		renderVariantsPanel(w, r, itemID, false)
	})

	// Lead time (days to receive a reorder) — feeds the inventory page's
	// per-item warn/reorder-suggestion thresholds (universaltill/ut-docs#85).
	// Plain integer, no currency conversion (unlike cost price above).
	mux.HandleFunc("POST /api/catalog/item-lead-time", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		itemID := strings.TrimSpace(r.Form.Get("panelItem"))
		raw := strings.TrimSpace(r.Form.Get("leadTimeDays"))
		if itemID == "" {
			http.Error(w, "item required", http.StatusBadRequest)
			return
		}
		var days int
		if raw != "" {
			n, err := strconv.Atoi(raw)
			// Upper bound is a sanity ceiling (universaltill/ut-docs#276):
			// without it, an absurdly large value (e.g. 999999999) makes
			// the inventory/digest "DaysLeft <= leadTimeDays" warning fire
			// permanently — a real, if self-inflicted, footgun.
			if err != nil || n < 0 || n > 365 {
				http.Error(w, "invalid lead time", http.StatusBadRequest)
				return
			}
			days = n
		}
		if err := repo.SetItemLeadTimeDays(r.Context(), itemID, days); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		renderVariantsPanel(w, r, itemID, false)
	})

	// Reorder level (the stock quantity below which an item counts as low) —
	// feeds the stock table's "Reorder at" column and the inventory page's
	// Low Stock list (GetLowStockItems, universaltill/ut-docs#2065). Plain
	// integer, same validation shape as lead time above.
	mux.HandleFunc("POST /api/catalog/item-reorder-level", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		itemID := strings.TrimSpace(r.Form.Get("panelItem"))
		raw := strings.TrimSpace(r.Form.Get("reorderLevel"))
		if itemID == "" {
			http.Error(w, "item required", http.StatusBadRequest)
			return
		}
		var level int
		if raw != "" {
			n, err := strconv.Atoi(raw)
			// Upper bound is a sanity ceiling, same rationale as item-cost's
			// 1_000_000 above (universaltill/ut-docs#276): nothing stops an
			// absurd value otherwise, and it's not a real limit on shop stock.
			if err != nil || n < 0 || n > 1_000_000 {
				http.Error(w, "invalid reorder level", http.StatusBadRequest)
				return
			}
			level = n
		}
		if err := repo.SetItemReorderLevel(r.Context(), itemID, level); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		renderVariantsPanel(w, r, itemID, false)
	})

	// The per-item editor: all of one item's variants and barcodes, editable.
	mux.HandleFunc("GET /api/catalog/item-variants", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagement(w, r) {
			return
		}
		renderVariantsPanel(w, r, strings.TrimSpace(r.URL.Query().Get("item_id")), false)
	})

	mux.HandleFunc("/catalog", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagementPage(w, r) {
			return
		}
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		items, err := repo.ListItems(r.Context())
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "catalog.error.server", err)
			return
		}
		cats, brands, err := listLookups(r.Context(), repo)
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "catalog.error.server", err)
			return
		}
		// ut-docs#2119: deliberately NOT the "cats" lookup just above —
		// that one is the item-edit form's unfiltered <select> (a
		// deactivated-but-still-referenced category must stay selectable
		// there, per ListActiveCategories' own doc comment) while the
		// category FILTER chip row must never offer a chip a shop owner
		// can browse into for a category that no longer exists to browse.
		// Two different lookups for two different reasons — do not merge
		// them into one shared call.
		categoryFilterOptions, err := repo.ListActiveCategories(r.Context())
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "catalog.error.server", err)
			return
		}
		// ut-docs#2140: a still-active child whose parent was deactivated
		// (and so is missing from the slice above) would otherwise get no
		// chip at all — see TopLevelForFilterChips' own doc comment.
		categoryFilterOptions = data.TopLevelForFilterChips(categoryFilterOptions)
		taxCodes, err := repo.ListAllTaxCodes(r.Context())
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "catalog.error.server", err)
			return
		}
		barcodes, _ := repo.ItemBarcodes(r.Context())
		variants, _ := repo.ItemVariants(r.Context())
		thumbnails, _ := repo.ItemThumbnails(r.Context())
		// ut-docs#2314: the catalog list/edit form must show each item's
		// CURRENT EFFECTIVE price (price_history-resolved), not raw
		// base_price, same resolution ItemVariantsForSale/the sale-screen
		// tiles already use (ut-docs#2258) — see buildCatalogRows' own doc
		// comment for what this overrides and why.
		itemIDs := make([]string, len(items))
		for i, itm := range items {
			itemIDs[i] = itm.ID
		}
		// ut-docs#2452: previously called unchunked with the error discarded
		// (`_`) — same bug family as #2318/#2451 (SQLite's bind-variable
		// ceiling, reachable here past ~32,766 items since ItemCurrentPrices
		// binds 1 arg/id). Chunking keeps this call comfortably under the
		// ceiling regardless of catalog size; a chunk failure now degrades
		// only that chunk's rows to base_price (buildCatalogRows' own
		// fallback) instead of silently losing every promotional price.
		currentPrices := map[string]int64{}
		for _, chunk := range data.ChunkStrings(itemIDs, data.IDChunkSize) {
			p, err := repo.ItemCurrentPrices(r.Context(), chunk)
			if err != nil {
				log.Printf("[catalog] current prices failed for a batch of %d item(s), those rows fall back to base_price: %v", len(chunk), err)
				continue
			}
			data.MergeMapInto(currentPrices, p)
		}
		// ut-docs#2090: whether this render is an /items-shell fragment
		// swap (true) or a bare/standalone page (false) — catalog.html's
		// own Modifiers/Option-sets top-action buttons and their
		// destinations' back-links use this to decide between an in-panel
		// htmx swap (only meaningful when #items-panel actually exists,
		// i.e. inside the shell) and a plain navigation.
		// ut-docs#2331: was a hardcoded English literal — ut-docs#2297/PR#1189's
		// sweep and its own check 10 both used a non-recursive
		// internal/pages/*.go glob, so this handler (one directory deeper,
		// internal/pages/catalog/) was never reached. Reuses nav.catalog,
		// the /items rail's own label key for this same destination.
		data := map[string]any{
			"title":                 httpx.T(httpx.RequestLocale(r), "nav.catalog"),
			"menuItems":             d.MenuSnapshot(),
			"theme":                 d.CurrentState().Theme,
			"Rows":                  buildCatalogRows(items, barcodes, variants, thumbnails, currentPrices),
			"Categories":            cats,
			"CategoryFilterOptions": categoryFilterOptions,
			"CategoryNodesJSON":     categoryFilterNodesJSON(categoryFilterOptions),
			"Brands":                brands,
			"TaxCodes":              taxCodes,
			"SyncPrimary":           d.SyncPrimaryURL(r.Context()),
			"BuiltinIcons":          catimport.BuiltinIcons(),
			"ItemColors":            catalogtypes.ItemColors(),
			"InItemsShell":          httpx.IsFragmentSwap(w, r),
		}
		catalogFiles := files(
			filepath.Join("web", "ui", "layouts", "base.html"),
			filepath.Join("web", "ui", "pages", "catalog.html"),
			filepath.Join("web", "ui", "partials", "nav.html"),
			filepath.Join("web", "ui", "partials", "bugreport_panel.html"),
			// ut-docs#2183: base.html now references {{ template "pos_alert" . }}
			// unconditionally — required in this parsed set or executing
			// "base" below (the non-fragment-swap branch) fails at render
			// time, same reasoning as bugreport_panel.html just above.
			filepath.Join("web", "ui", "partials", "pos_alert.html"),
			filepath.Join("web", "ui", "partials", "catalog_table.html"),
			filepath.Join("web", "ui", "partials", "catalog_row.html"),
			filepath.Join("web", "ui", "partials", "catalog_variants.html"),
			filepath.Join("web", "ui", "partials", "category_filter.html"),
			filepath.Join("web", "ui", "partials", "category_filter_popover.html"),
		)
		// ut-docs#1950: /catalog is also the /items rail's default ("Library")
		// section — an htmx request from that panel (NOT a stale history
		// restore, see httpx.IsFragmentSwap) gets just the "content" block
		// plus an out-of-band refresh of the rail itself, so its is-current
		// highlight follows the click; a plain browser GET (deep link) still
		// gets the exact same full standalone page as before this card.
		if httpx.IsFragmentSwap(w, r) {
			httpx.RenderWith(catalogFiles, funcs)("content", data)(w, r)
			itemsnav.WriteRailOOB(w, r, funcs, "/catalog", itemsnav.Resolve(httpx.RequestLocale(r), d.ItemsAmendmentsSnapshot()))
			return
		}
		httpx.RenderWith(catalogFiles, funcs)("base", data)(w, r)
	})

	// The shop-wide modifiers screen — originally read-only browse
	// (ut-docs#1899); ut-docs#1957 made it the full CRUD home for modifier
	// groups, moved out of the per-item admin panel (catalog_variants.html)
	// after a product-owner review found the old placement confusing ("I
	// saw the customization groups under the variants"); ADR-0101
	// (ut-docs#2399) made it the home of the group itself: one card per
	// group, created here with no item, assigned to categories and items
	// from the card. A deactivated group/option is still shown so a
	// manager can reactivate it.
	mux.HandleFunc("/modifiers", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagementPage(w, r) {
			return
		}
		modifiersData, err := modifiersPageData(r.Context())
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "modifiers.error.server", err)
			return
		}
		// ut-docs#2211: was a hardcoded English literal — missed by
		// ut-docs#2297/PR#1189's `internal/pages/*.go` sweep because
		// this handler lives one directory deeper, in
		// internal/pages/catalog/. Same page-title bug, same fix.
		modifiersData["title"] = httpx.T(httpx.RequestLocale(r), "modifiers.title")
		modifiersData["menuItems"] = d.MenuSnapshot()
		modifiersData["theme"] = d.CurrentState().Theme
		modifiersData["InItemsShell"] = httpx.IsFragmentSwap(w, r)
		// ut-docs#1950: same /items rail embedding as /catalog above.
		if httpx.IsFragmentSwap(w, r) {
			httpx.RenderContentFragment("ui/pages/modifiers.html", modifiersData)(w, r)
			itemsnav.WriteRailOOB(w, r, httpx.FuncsFor(httpx.RequestLocale(r)), "/modifiers", itemsnav.Resolve(httpx.RequestLocale(r), d.ItemsAmendmentsSnapshot()))
			return
		}
		httpx.Render("ui/pages/modifiers.html", modifiersData)(w, r)
	})

	// Reusable option sets (ut-docs#1900): a shop-wide screen where a
	// merchant defines a named, ordered axis once ("Size: S / M / L") and
	// the per-item panel (catalog_variants.html) applies up to two of them
	// to generate the item's real item_variants range in one step. Kept
	// separate from checkout-time modifiers (/modifiers, ADR-0020), which
	// change nothing here.
	mux.HandleFunc("GET /catalog/option-sets", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagementPage(w, r) {
			return
		}
		sets, err := data.NewOptionSetRepo(d.Db).ListOptionSets(r.Context())
		if err != nil {
			httpx.RenderError(w, r, http.StatusInternalServerError, "catalog.error.server", err)
			return
		}
		// ut-docs#2331: same hardcoded-literal gap as /catalog above, same
		// fix. Reuses items.option_sets.name, the /items rail's own label
		// key for this destination.
		optionSetsData := map[string]any{
			"title":        httpx.T(httpx.RequestLocale(r), "items.option_sets.name"),
			"menuItems":    d.MenuSnapshot(),
			"theme":        d.CurrentState().Theme,
			"Sets":         sets,
			"InItemsShell": httpx.IsFragmentSwap(w, r),
		}
		// ut-docs#1950: same /items rail embedding as /catalog above.
		if httpx.IsFragmentSwap(w, r) {
			httpx.RenderContentFragment("ui/pages/option_sets.html", optionSetsData)(w, r)
			itemsnav.WriteRailOOB(w, r, httpx.FuncsFor(httpx.RequestLocale(r)), "/catalog/option-sets", itemsnav.Resolve(httpx.RequestLocale(r), d.ItemsAmendmentsSnapshot()))
			return
		}
		httpx.Render("ui/pages/option_sets.html", optionSetsData)(w, r)
	})

	// renderOptionSetsList answers a mutation on the option-sets screen with
	// its re-rendered list fragment (#option-sets-list, outerHTML swap).
	renderOptionSetsList := func(w http.ResponseWriter, r *http.Request) {
		sets, err := data.NewOptionSetRepo(d.Db).ListOptionSets(r.Context())
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		funcs := httpx.FuncsFor(httpx.ResolveLocale(w, r))
		httpx.RenderWith(files(
			filepath.Join("web", "ui", "pages", "option_sets.html"),
		), funcs)("option_sets_list", map[string]any{"Sets": sets})(w, r)
	}

	// Create a shop-wide option set. Not item-scoped: called from the
	// option-sets screen (re-renders its list); a panelItem, if one ever
	// arrives, re-renders that item's panel instead so the new set shows up
	// in its checkbox row immediately.
	mux.HandleFunc("POST /api/catalog/option-set", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		name := strings.TrimSpace(r.Form.Get("name"))
		if name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		if _, err := data.NewOptionSetRepo(d.Db).CreateOptionSet(r.Context(), name); err != nil {
			optionSetAwareError(w, r, http.StatusBadRequest, err)
			return
		}
		if panelItem := strings.TrimSpace(r.Form.Get("panelItem")); panelItem != "" {
			renderVariantsPanel(w, r, panelItem, false)
			return
		}
		renderOptionSetsList(w, r)
	})

	// Append a value to an option set (sort_order = max + 1).
	mux.HandleFunc("POST /api/catalog/option-set-value", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		setID := strings.TrimSpace(r.Form.Get("optionSetId"))
		value := strings.TrimSpace(r.Form.Get("value"))
		if setID == "" || value == "" {
			http.Error(w, "optionSetId and value required", http.StatusBadRequest)
			return
		}
		if _, err := data.NewOptionSetRepo(d.Db).AddOptionSetValue(r.Context(), setID, value); err != nil {
			optionSetAwareError(w, r, http.StatusBadRequest, err)
			return
		}
		if panelItem := strings.TrimSpace(r.Form.Get("panelItem")); panelItem != "" {
			renderVariantsPanel(w, r, panelItem, false)
			return
		}
		renderOptionSetsList(w, r)
	})

	// Apply the checked option sets (repeated optionSetIds, checkbox DOM
	// order = axis order) to the panel's item. The repo enforces the
	// at-most-two rule as a real constraint; the panel's checkbox row also
	// disables a third box client-side, but that is a convenience, not the
	// guard.
	mux.HandleFunc("POST /api/catalog/item/option-sets", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		itemID := strings.TrimSpace(r.Form.Get("panelItem"))
		if itemID == "" {
			http.Error(w, "panelItem required", http.StatusBadRequest)
			return
		}
		var ids []string
		for _, id := range r.Form["optionSetIds"] {
			if id = strings.TrimSpace(id); id != "" {
				ids = append(ids, id)
			}
		}
		if err := data.NewOptionSetRepo(d.Db).ApplyOptionSetsToItem(r.Context(), itemID, ids); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", err)
			return
		}
		renderVariantsPanel(w, r, itemID, false)
	})

	// Run the generator: one item_variants row per missing combination of
	// the item's applied sets' values, never duplicating or removing an
	// existing one (safe to re-run after adding a value — the
	// ut-docs#1839 re-import-duplication class of bug is exactly what the
	// item_variant_options link table exists to prevent). Answers with the
	// panel plus a "N variant(s) created" line; the item's table row rides
	// along OOB since its variant summary just changed.
	mux.HandleFunc("POST /api/catalog/item/generate-variants", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		itemID := strings.TrimSpace(r.Form.Get("panelItem"))
		if itemID == "" {
			http.Error(w, "panelItem required", http.StatusBadRequest)
			return
		}
		created, err := data.NewOptionSetRepo(d.Db).GenerateVariants(r.Context(), itemID)
		if errors.Is(err, data.ErrNoOptionSetsApplied) {
			// Not a failure — the operator just pressed Generate before
			// Apply. Say so in the panel rather than with an error toast.
			renderVariantsPanelWith(w, r, itemID, false, map[string]any{"GenerateNoSets": true})
			return
		}
		if err != nil {
			skuAwareError(w, r, http.StatusBadRequest, err)
			return
		}
		renderVariantsPanelWith(w, r, itemID, true, map[string]any{"Generated": true, "GeneratedCount": created})
	})

	mux.HandleFunc("/api/catalog/item", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		itemInput, err := parseItemInput(r)
		if err != nil {
			// parseItemInput/validateLookups return clean, bounded,
			// hand-written validation errors ("name and price required",
			// "invalid categories id", …) — never raw SQL/driver text or
			// an internal ID, so these are out of ut-docs#316's scope
			// (unlike the pos./data.-layer errors below, which can wrap
			// real DB errors and go through the translated+logged path).
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := validateLookups(r.Context(), repo, itemInput); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		itemID, err := pos.CreateItem(r.Context(), d.Db, itemInput)
		if err != nil {
			skuAwareError(w, r, http.StatusBadRequest, err)
			return
		}
		// Auto-fill flow: attach the looked-up barcode so the new item is
		// instantly scannable, and save the source image as the tile thumb.
		if code := strings.TrimSpace(r.Form.Get("barcode")); code != "" {
			in := pos.BarcodeInput{Barcode: code, ItemID: itemID, IsPrimary: true}
			// See the matching comment at /api/catalog/barcode (ut-docs#948 F1).
			in.BarcodeType = plainBarcodeTypeFor(r, code)
			if err := pos.AddBarcode(r.Context(), d.Db, in); err != nil {
				locale := httpx.ResolveLocale(w, r)
				if errors.Is(err, data.ErrInvalidEAN13) {
					http.Error(w, httpx.T(locale, "catalog.error.item_created_invalid_ean13"), http.StatusBadRequest)
					return
				}
				// Name the conflicting item/variant for the operator instead
				// of the raw internal ID that used to leak here (ut-docs#303).
				reason := common.FriendlyBarcodeConflict(r.Context(), repo, locale, err)
				http.Error(w, fmt.Sprintf(httpx.T(locale, "catalog.error.item_created_barcode_failed"), reason), http.StatusBadRequest)
				return
			}
		}
		if imgURL := strings.TrimSpace(r.Form.Get("imageUrl")); imgURL != "" {
			if err := saveLookupImage(r.Context(), lookupClient, itemID, imgURL); err != nil {
				log.Printf("[catalog] lookup image for item %s skipped: %v", itemID, err)
			} else if err := repo.SetItemThumbnail(r.Context(), itemID, "/public/assets/items/"+itemID+"/thumb.png"); err != nil {
				// Same review-F2 reasoning as the manual upload handler
				// above: the photo is safely on disk regardless, this only
				// keeps item_images (POS grid/basket/self-order/
				// suggestions) in sync with what the admin table shows.
				log.Printf("[catalog] record item_images thumbnail for %s: %v", itemID, err)
			}
		}
		writeRowOOB(w, r, itemID, true)
	})

	// Update item
	mux.HandleFunc("/api/catalog/item/update", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		itemInput, err := parseItemInput(r)
		if err != nil {
			// See the matching comment in /api/catalog/item above.
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		itemInput.ID = strings.TrimSpace(r.Form.Get("id"))
		if err := validateLookups(r.Context(), repo, itemInput); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		// Previous active state decides the OOB mode: an item that was
		// INACTIVE before this update has no row in the table (inactive
		// items are never rendered), so a reactivating save must APPEND its
		// row — an in-place update would target a missing id, which htmx
		// answers with a console error and no visible row. Reachable for
		// real: deactivate a row while the edit form still holds that item,
		// then save. Read+write run atomically (ut-docs#1399) so two
		// genuinely concurrent updates on the same item can't both read the
		// pre-update state and both append a row — see
		// UpdateItemReturningWasActive's doc comment.
		wasActive, err := pos.UpdateItemReturningWasActive(r.Context(), d.Db, itemInput)
		if err != nil {
			skuAwareError(w, r, http.StatusBadRequest, err)
			return
		}
		writeRowOOB(w, r, itemInput.ID, !wasActive)
	})

	// Deactivate item
	mux.HandleFunc("/api/catalog/item/deactivate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		itemID := strings.TrimSpace(r.Form.Get("id"))
		if itemID == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := pos.DeactivateItem(r.Context(), d.Db, itemID); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", err)
			return
		}
		writeRowOOB(w, r, itemID, false)
	})

	// Create or update variant (if id present => update)
	mux.HandleFunc("/api/catalog/variant", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		itemID := strings.TrimSpace(r.Form.Get("itemId"))
		if itemID == "" {
			http.Error(w, "itemId required", http.StatusBadRequest)
			return
		}
		priceStr := strings.TrimSpace(r.Form.Get("price"))
		if priceStr == "" {
			http.Error(w, "price required", http.StatusBadRequest)
			return
		}
		price, err := strconv.ParseInt(priceStr, 10, 64)
		if err != nil {
			http.Error(w, "invalid price", http.StatusBadRequest)
			return
		}
		// Checkbox semantics: an unchecked box submits nothing, so the panel
		// pairs it with a hidden isActive=0 — active only when a "1" arrived.
		active := r.Form.Get("isActive") != "0"
		if vals := r.Form["isActive"]; len(vals) > 1 {
			active = false
			for _, v := range vals {
				if v == "1" {
					active = true
				}
			}
		}
		vInput := pos.VariantInput{
			ID:       strings.TrimSpace(r.Form.Get("id")),
			ItemID:   itemID,
			SKU:      strings.TrimSpace(r.Form.Get("sku")),
			Name:     strings.TrimSpace(r.Form.Get("name")),
			Price:    price,
			IsActive: active,
		}
		if costStr := strings.TrimSpace(r.Form.Get("costPrice")); costStr != "" {
			if c, err := strconv.ParseInt(costStr, 10, 64); err == nil {
				vInput.CostPrice = &c
			} else {
				http.Error(w, "invalid costPrice", http.StatusBadRequest)
				return
			}
		}
		if vInput.ID == "" {
			if _, err := pos.CreateVariant(r.Context(), d.Db, vInput); err != nil {
				skuAwareError(w, r, http.StatusBadRequest, err)
				return
			}
		} else {
			if err := pos.UpdateVariant(r.Context(), d.Db, vInput); err != nil {
				skuAwareError(w, r, http.StatusBadRequest, err)
				return
			}
		}
		if panelItem := strings.TrimSpace(r.Form.Get("panelItem")); panelItem != "" {
			renderVariantsPanel(w, r, panelItem, true)
			return
		}
		writeRowOOB(w, r, itemID, false)
	})

	// maxModifierSelect bounds min_select/max_select (ut-docs#2376): the
	// item_modifier_groups CHECK constraint only requires
	// min_select >= 0 AND max_select >= min_select, no upper bound, so an
	// absurd value (e.g. 999999999) would otherwise be accepted and render
	// a nonsensical "choose between 0 and 999999999" picker on the sale
	// screen (pos_modifiers_api.go's selection-count check). Not
	// money/tax-relevant — mirrored in cloudsync_wire.go's
	// cloudUpsertModifierGroup (same value, separate package) and
	// ut-cloud's internal/claims.maxModifierGroupSelect (separate repo).
	const maxModifierSelect = 50

	// Create or update a modifier group (ADR-0020) — id present = update,
	// absent = create. Deactivating (isActive toggle) is the reversible
	// way to take a group off sale; the hard delete is its own route
	// (/api/catalog/modifier-group/delete below) behind a confirm.
	//
	// ADR-0101 (ut-docs#2399): itemId is OPTIONAL on create. /modifiers'
	// own create form sends none — the group is shop-wide and gets its
	// assignments afterwards from its card. A caller that does send one
	// (nothing in web/ui does today; the item editor is attach-only since
	// ut-docs#2330) gets the group created AND linked to that item, in
	// that order — two repo calls, not one transaction: a link failure
	// after a successful create leaves a valid, unassigned group behind
	// (a state /modifiers shows and the merchant can finish by hand),
	// never a half-written row.
	mux.HandleFunc("/api/catalog/modifier-group", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		itemID := strings.TrimSpace(r.Form.Get("itemId"))
		name := strings.TrimSpace(r.Form.Get("name"))
		if name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}
		minSelect, _ := strconv.Atoi(strings.TrimSpace(r.Form.Get("minSelect")))
		maxSelect, err := strconv.Atoi(strings.TrimSpace(r.Form.Get("maxSelect")))
		if err != nil || maxSelect < 1 {
			maxSelect = 1
		}
		sortOrder, _ := strconv.Atoi(strings.TrimSpace(r.Form.Get("sortOrder")))
		required := r.Form.Get("required") == "1"
		if required && minSelect < 1 {
			minSelect = 1 // a required group must ask for at least one pick
		}
		if minSelect > maxModifierSelect || maxSelect > maxModifierSelect {
			common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog",
				fmt.Errorf("min_select/max_select must be <= %d", maxModifierSelect))
			return
		}
		active := formCheckboxActive(r)

		modRepo := data.NewModifierRepo(d.Db)
		groupID := strings.TrimSpace(r.Form.Get("id"))
		if groupID == "" {
			newID := uuid.NewString()
			if _, err := modRepo.CreateGroup(r.Context(), newID, name, required, minSelect, maxSelect, sortOrder); err != nil {
				common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", err)
				return
			}
			if itemID != "" {
				linkSort, err := modRepo.NextGroupSortOrderForItem(r.Context(), itemID)
				if err != nil {
					common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "modifiers.error.server", "catalog", err)
					return
				}
				if err := modRepo.LinkGroupToItem(r.Context(), itemID, newID, linkSort); err != nil {
					common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", err)
					return
				}
			}
		} else {
			if err := modRepo.UpdateGroup(r.Context(), groupID, name, required, minSelect, maxSelect, sortOrder, active); err != nil {
				common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", err)
				return
			}
		}
		renderModifierMutationResult(w, r, itemID, http.StatusOK, "")
	})

	// Create or update a modifier option (ADR-0020). majorPrice is entered
	// in the shop's display currency and converted to minor units here —
	// same convention as variant price entry.
	mux.HandleFunc("/api/catalog/modifier-option", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		groupID := strings.TrimSpace(r.Form.Get("groupId"))
		// itemId is only the re-render hint for the legacy #catalog-variants
		// fallback in renderModifierMutationResult; /modifiers' own option
		// forms (ADR-0101) carry none, since a group has no item.
		itemID := strings.TrimSpace(r.Form.Get("itemId"))
		name := strings.TrimSpace(r.Form.Get("name"))
		if groupID == "" || name == "" {
			http.Error(w, "groupId and name required", http.StatusBadRequest)
			return
		}
		priceDeltaMinor := int64(0)
		if majorStr := strings.TrimSpace(r.Form.Get("priceDeltaMajor")); majorStr != "" {
			major, err := strconv.ParseFloat(majorStr, 64)
			if err != nil || major < 0 {
				http.Error(w, "invalid priceDeltaMajor", http.StatusBadRequest)
				return
			}
			// Decimal-aware: a 0-decimal currency (IRR/IRT/IQD/AFN/JPY, all
			// supported — see httpx.currencies) has no minor-unit
			// subdivision at all, so a hardcoded *100 would inflate every
			// price 100x for those shops. Matches the same
			// currency.Decimals the template already uses for this
			// field's pattern="" attribute (ut-docs#1284 moved it from
			// type="number" step="" to type="text" pattern="").
			decimals := httpx.CurrencyByCode(d.CurrentState().Currency).Decimals
			priceDeltaMinor = int64(math.Round(major * math.Pow(10, float64(decimals))))
		}
		sortOrder, _ := strconv.Atoi(strings.TrimSpace(r.Form.Get("sortOrder")))
		active := formCheckboxActive(r)

		modRepo := data.NewModifierRepo(d.Db)
		optionID := strings.TrimSpace(r.Form.Get("id"))
		if optionID == "" {
			if _, err := modRepo.CreateOption(r.Context(), uuid.NewString(), groupID, name, priceDeltaMinor, sortOrder); err != nil {
				common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", err)
				return
			}
		} else {
			if err := modRepo.UpdateOption(r.Context(), optionID, name, priceDeltaMinor, sortOrder, active); err != nil {
				common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", err)
				return
			}
		}
		renderModifierMutationResult(w, r, itemID, http.StatusOK, "")
	})

	// Attach an existing modifier group to another item (ut-docs#2046 /
	// ADR-0090 §5) — the write path for the many-to-many relationship
	// ADR-0090 laid the schema for.
	//
	// Re-validates against ListAttachableModifierGroups server-side
	// (independent-review finding) rather than trusting the submitted
	// groupId blindly: a stale picker — open in a browser tab since before
	// someone else deactivated the group, or attached it to this same item
	// from another tab — must not be able to attach an inactive group, or
	// silently re-order an already-attached one via LinkGroupToItem's own
	// ON CONFLICT DO UPDATE SET sort_order (that clause exists to let a
	// resubmit settle safely, not to let a stale form move a live link).
	mux.HandleFunc("/api/catalog/modifier-group/attach", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		itemID := strings.TrimSpace(r.Form.Get("itemId"))
		// ut-docs#2330: the item-editor's attach-only surface is a multi-
		// select — one submission can carry several groupId values (a
		// checkbox list, all under the same field name). A single-group
		// submission (the /modifiers page's own picker, and every existing
		// caller/test) is just the one-element case of the same slice, so
		// this stays fully backward compatible with r.Form.Get's old
		// single-value behavior.
		var groupIDs []string
		seen := map[string]bool{}
		for _, raw := range r.Form["groupId"] {
			id := strings.TrimSpace(raw)
			if id == "" || seen[id] {
				continue
			}
			seen[id] = true
			groupIDs = append(groupIDs, id)
		}
		if itemID == "" {
			// ut-docs#2399, independent-review finding: on /modifiers every
			// group card now carries an "Add item" picker whose value that
			// matters is a HIDDEN itemId resolved from a free-text search —
			// `required` on a hidden input is ignored by constraint
			// validation, so a typo that resolves to nothing reaches this
			// handler. A text/plain http.Error here is never swapped in by
			// app.js's htmx:beforeSwap (same reasoning as the groupIDs
			// branch just below), i.e. tapping "Add item" would do nothing
			// visible. Route through the Notice-carrying re-render instead.
			renderModifierMutationResult(w, r, "", http.StatusBadRequest, httpx.T(httpx.RequestLocale(r), "modifiers.pick_item"))
			return
		}
		if len(groupIDs) == 0 {
			// ut-docs#2330, independent-review finding: the item-editor's
			// checkbox list has no client-side `required` (a `<select
			// required>` can enforce that on ONE field; there is no
			// equivalent single-attribute guard for "at least one of these
			// checkboxes"), so submitting with nothing ticked is a real,
			// reachable UI state — not just a malformed request. A plain
			// http.Error answers text/plain, which app.js's htmx:beforeSwap
			// never force-swaps in (same reasoning as every other refusal in
			// this file), so answering that way here would be a completely
			// silent no-op on tapping "Attach existing group". Route through
			// the same Notice-carrying re-render as every other refusal.
			renderModifierMutationResult(w, r, itemID, http.StatusBadRequest, httpx.T(httpx.RequestLocale(r), "catalog.error.invalid_request"))
			return
		}
		attachable, err := modRepo.ListAttachableModifierGroups(r.Context(), itemID)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "modifiers.error.server", "catalog", err)
			return
		}
		attachableIDs := make(map[string]bool, len(attachable))
		for _, g := range attachable {
			attachableIDs[g.ID] = true
		}
		var valid []string
		for _, id := range groupIDs {
			if attachableIDs[id] {
				valid = append(valid, id)
			}
		}
		if len(valid) == 0 {
			renderModifierMutationResult(w, r, itemID, http.StatusConflict, httpx.T(httpx.RequestLocale(r), "catalog.error.invalid_request"))
			return
		}
		// ut-docs#2330, independent-review finding: a multi-select submission
		// can be PARTLY stale (e.g. someone else deactivated one of several
		// checked groups from another tab) without being entirely invalid —
		// attach the ones that are still good rather than refusing the whole
		// request, but say so, rather than a silent 200 that only attached
		// some of what was checked.
		partial := len(valid) != len(groupIDs)
		for _, groupID := range valid {
			// Appended after itemID's own existing groups (independent-review
			// finding — see NextGroupSortOrderForItem's own doc comment on
			// why this must be MAX(sort_order)+1 per item, not a plain count
			// of either side of the relationship). Recomputed per group so
			// each of a multi-select's attachments gets its own increasing
			// sort_order rather than all colliding on the same value.
			sortOrder, err := modRepo.NextGroupSortOrderForItem(r.Context(), itemID)
			if err != nil {
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "modifiers.error.server", "catalog", err)
				return
			}
			if err := modRepo.LinkGroupToItem(r.Context(), itemID, groupID, sortOrder); err != nil {
				common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", err)
				return
			}
		}
		notice := ""
		if partial {
			notice = httpx.T(httpx.RequestLocale(r), "catalog.error.invalid_request")
		}
		renderModifierMutationResult(w, r, itemID, http.StatusOK, notice)
	})

	// Detach a modifier group from ONE item — the item's link row only,
	// never the group (that is /delete below). Since ADR-0101
	// (ut-docs#2399) this is unconditional, including the group's last
	// item link: a group with no assignment is a valid state, listed and
	// editable on /modifiers (which reads the group table itself, not the
	// links) and simply not offered at checkout. The ut-docs#2046 last-link
	// refusal existed only because the old per-item /modifiers listing
	// could not show an orphan, and went with that listing.
	mux.HandleFunc("/api/catalog/modifier-group/detach", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		itemID := strings.TrimSpace(r.Form.Get("itemId"))
		groupID := strings.TrimSpace(r.Form.Get("groupId"))
		if itemID == "" || groupID == "" {
			http.Error(w, "itemId and groupId required", http.StatusBadRequest)
			return
		}
		if err := modRepo.UnlinkGroupFromItem(r.Context(), itemID, groupID); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "modifiers.error.server", "catalog", err)
			return
		}
		renderModifierMutationResult(w, r, itemID, http.StatusOK, "")
	})

	// Assign / unassign a modifier group to a CATEGORY from the group's
	// own card on /modifiers (ADR-0101 §3, ut-docs#2399) — one link row at
	// a time, the same category_modifier_group_links rows the category
	// editor's multi-select Save (SetCategoryModifierGroups, ut-docs#2284)
	// manages wholesale from the other side. Gated and answered exactly
	// like the item-side attach/detach above: requireCatalogManagement,
	// requirePrimary (the link tables are admin-synced), and
	// renderModifierMutationResult so the card re-renders and an open sale
	// screen refetches its tile gate (HX-Trigger: modifiers-changed).
	// A groupId/categoryId that does not exist fails the link row's FK and
	// comes back as a 400, not a 500 — a stale card must not look like an
	// outage.
	categoryLinkToggle := func(action string, apply func(context.Context, string, string) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if !requireCatalogManagement(w, r) {
				return
			}
			if !requirePrimary(w, r, "catalog.error.replica_use_primary") {
				return
			}
			_ = r.ParseForm()
			groupID := strings.TrimSpace(r.Form.Get("groupId"))
			categoryID := strings.TrimSpace(r.Form.Get("categoryId"))
			if groupID == "" || categoryID == "" {
				http.Error(w, "groupId and categoryId required", http.StatusBadRequest)
				return
			}
			if err := apply(r.Context(), categoryID, groupID); err != nil {
				common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog "+action, err)
				return
			}
			renderModifierMutationResult(w, r, "", http.StatusOK, "")
		}
	}
	mux.HandleFunc("/api/catalog/modifier-group/attach-category", categoryLinkToggle("attach-category", func(ctx context.Context, categoryID, groupID string) error {
		// Appended after the category's existing groups (same
		// MAX(sort_order)+1 rule as the item-side attach — see
		// NextGroupSortOrderForItem's own doc comment on why not a count).
		sortOrder, err := modRepo.NextGroupSortOrderForCategory(ctx, categoryID)
		if err != nil {
			return err
		}
		return modRepo.LinkGroupToCategory(ctx, categoryID, groupID, sortOrder)
	}))
	mux.HandleFunc("/api/catalog/modifier-group/detach-category", categoryLinkToggle("detach-category", modRepo.UnlinkGroupFromCategory))

	// Delete a modifier group EVERYWHERE (ADR-0101 Decision 2, ut-docs#2399)
	// — the group row, and by cascade its options, every item link, every
	// category link and every opt-out. The card's button carries an
	// hx-confirm (modifiers.delete_confirm) that says exactly that, and
	// that past sales keep the modifiers they recorded
	// (sale_line_modifiers snapshots names/prices and has no FK onto the
	// group). Deactivating stays the reversible alternative. Same gates
	// and same re-render dispatch as every other group mutation.
	mux.HandleFunc("/api/catalog/modifier-group/delete", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		groupID := strings.TrimSpace(r.Form.Get("groupId"))
		if groupID == "" {
			http.Error(w, "groupId required", http.StatusBadRequest)
			return
		}
		if err := modRepo.DeleteGroup(r.Context(), groupID); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "modifiers.error.server", "catalog delete-group", err)
			return
		}
		renderModifierMutationResult(w, r, "", http.StatusOK, "")
	})

	// Delete every UNASSIGNED modifier group in one action (ut-docs#2406,
	// follow-up from the ADR-0101/#2399 review's finding L5): a shop that
	// cleans up hundreds of obsolete items, each with its own single-use
	// group, ends up with hundreds of orphaned cards on /modifiers and only
	// the per-card Delete above to clear them one at a time. "Unassigned"
	// is the exact same definition modifiersPageData's UnassignedCount and
	// this page's own .modifier-unassigned hint already use — no category
	// link, no item link — so this can never delete a group still offered
	// anywhere. Same gates and same cascade/past-sales guarantee as the
	// single-group delete above.
	//
	// ut-docs#2421: the confirm dialog's count is server-rendered from the
	// *previous* GET (modifiersPageData's own independent Go-side tally),
	// so it can go stale before the click — another operator (a second
	// tab, another till, someone detaching a group's last link elsewhere)
	// changes what's unassigned, or simply creates a new group, which is
	// unassigned by definition. Re-check a fresh count against what the
	// button's hx-vals carried back (same re-validate-server-side shape as
	// #2046's attach-picker 409) and refuse rather than silently deleting
	// more than the operator confirmed.
	mux.HandleFunc("/api/catalog/modifier-group/delete-unassigned", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		expected, err := strconv.Atoi(strings.TrimSpace(r.Form.Get("expectedCount")))
		if err != nil {
			renderModifierMutationResult(w, r, "", http.StatusConflict, httpx.T(httpx.RequestLocale(r), "modifiers.delete_unassigned_stale"))
			return
		}
		live, err := modRepo.CountUnassignedGroups(r.Context())
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "modifiers.error.server", "catalog delete-unassigned", err)
			return
		}
		if live != expected {
			renderModifierMutationResult(w, r, "", http.StatusConflict, httpx.T(httpx.RequestLocale(r), "modifiers.delete_unassigned_stale"))
			return
		}
		if _, err := modRepo.DeleteUnassignedGroups(r.Context()); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "modifiers.error.server", "catalog delete-unassigned", err)
			return
		}
		renderModifierMutationResult(w, r, "", http.StatusOK, "")
	})

	// Opt an item OUT of / back IN to a category-inherited modifier group
	// (ADR-0094 Decision 2, ut-docs#2284). Deliberately NOT the detach
	// route above: an opt-out is a presence row in
	// item_modifier_group_opt_outs that only ever suppresses the CATEGORY
	// attachment — a group the item is ALSO directly linked to keeps that
	// link and stays offered (see OptOutItemFromGroup's own doc comment for
	// why the two must never be conflated: an unlink removes a direct
	// assignment, an opt-out only declines an inherited one).
	// Answered through renderModifierMutationResult like every other group
	// mutation, so the item-scoped dialog re-renders and the sale screen's
	// tile gate refetches (HX-Trigger: modifiers-changed).
	optToggle := func(action string, apply func(context.Context, string, string) error) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
				return
			}
			if !requireCatalogManagement(w, r) {
				return
			}
			if !requirePrimary(w, r, "catalog.error.replica_use_primary") {
				return
			}
			_ = r.ParseForm()
			itemID := strings.TrimSpace(r.Form.Get("itemId"))
			groupID := strings.TrimSpace(r.Form.Get("groupId"))
			if itemID == "" || groupID == "" {
				http.Error(w, "itemId and groupId required", http.StatusBadRequest)
				return
			}
			if err := apply(r.Context(), itemID, groupID); err != nil {
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "modifiers.error.server", "catalog "+action, err)
				return
			}
			renderModifierMutationResult(w, r, itemID, http.StatusOK, "")
		}
	}
	mux.HandleFunc("/api/catalog/modifier-group/opt-out", optToggle("opt-out", modRepo.OptOutItemFromGroup))
	mux.HandleFunc("/api/catalog/modifier-group/opt-in", optToggle("opt-in", modRepo.OptInItemToGroup))

	// Item-level kitchen-station override from the item editor
	// (ut-docs#2284): the SAME replace-all write kitchen_stations_page.go's
	// POST /api/kitchen-stations/routes/items/{itemID} does from the
	// station's side (every ticked station_id; none ticked clears the
	// override and the item follows its category again) — one write path,
	// SetItemStationRoutes, so the two screens can never disagree. Station
	// ids are checked against the shop's stations first: a stale panel must
	// not be able to route an item to a station that no longer exists
	// (the FK would refuse anyway, but as an opaque 500, not this 400).
	// item_station_routes is an adminTables entry, so a satellite picks the
	// change up on its next admin pull like the category-side write.
	mux.HandleFunc("/api/catalog/item-station-routes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		itemID := strings.TrimSpace(r.Form.Get("itemId"))
		if itemID == "" {
			http.Error(w, "itemId required", http.StatusBadRequest)
			return
		}
		stations, err := posRepo.ListKitchenStations(r.Context())
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		known := make(map[string]bool, len(stations))
		for _, s := range stations {
			known[s.ID] = true
		}
		var stationIDs []string
		for _, id := range r.Form["station_id"] {
			id = strings.TrimSpace(id)
			if id == "" {
				continue
			}
			if !known[id] {
				common.LocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request")
				return
			}
			stationIDs = append(stationIDs, id)
		}
		if err := posRepo.SetItemStationRoutes(r.Context(), itemID, stationIDs); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		renderVariantsPanel(w, r, strings.TrimSpace(r.Form.Get("panelItem")), false)
	})

	// Deactivate variant
	mux.HandleFunc("/api/catalog/variant/deactivate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		variantID := strings.TrimSpace(r.Form.Get("id"))
		if variantID == "" {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		if err := pos.DeactivateVariant(r.Context(), d.Db, variantID); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", err)
			return
		}
		if panelItem := strings.TrimSpace(r.Form.Get("panelItem")); panelItem != "" {
			renderVariantsPanel(w, r, panelItem, true)
			return
		}
		// No panel open: the form carries only the variant id, so resolve
		// the parent item whose row summary must drop this variant
		// (soft-deactivated rows still resolve). A lookup failure only
		// costs the row refresh — the deactivation itself already landed.
		if itemID, ok, err := repo.ItemIDForVariant(r.Context(), variantID); err == nil && ok {
			writeRowOOB(w, r, itemID, false)
		} else if err != nil {
			log.Printf("[catalog] resolve item for variant %s: %v", variantID, err)
		}
	})

	// Item image upload → web/public/assets/items/<id>/thumb.png (the same
	// convention the product tiles and designer use).
	//
	// Deliberately NOT requirePrimary-gated (ut-docs#1689): item_images is
	// explicitly excluded from sync_admin_repo.go's adminTables ("files
	// don't travel — D2 limit"), so a photo uploaded on a replica is a
	// replica-local fact, not a synced row that would silently revert on
	// the next admin pull — unlike items/item_variants/item_barcodes/
	// variant_barcodes below, which are.
	mux.HandleFunc("POST /api/catalog/item/image", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagement(w, r) {
			return
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			common.LocalizedError(w, r, http.StatusBadRequest, "common.error.invalid_upload")
			return
		}
		itemID := strings.TrimSpace(r.Form.Get("item_id"))
		if itemID == "" || strings.ContainsAny(itemID, "/\\.") {
			http.Error(w, "valid item_id required", http.StatusBadRequest)
			return
		}
		if ok, err := repo.ItemExists(r.Context(), itemID); err != nil || !ok {
			http.Error(w, "item not found", http.StatusNotFound)
			return
		}
		file, _, err := r.FormFile("image")
		if err != nil {
			http.Error(w, "image file required", http.StatusBadRequest)
			return
		}
		defer file.Close()
		raw, readErr := io.ReadAll(io.LimitReader(file, 10<<20))
		if readErr != nil {
			common.LocalizedError(w, r, http.StatusBadRequest, "catalog.error.image_invalid")
			return
		}
		// ut-docs#2500: decode/downscale/write is imaging.PrepareThumb +
		// WriteThumbPNG, shared with the variant photo below and the
		// category image (categories_page.go).
		img, err := imaging.PrepareThumb(raw)
		if err != nil {
			common.LocalizedError(w, r, http.StatusBadRequest, ThumbErrorKey(err))
			return
		}
		if err := imaging.WriteThumbPNG(img, filepath.Join(paths.Data("public", "assets", "items", itemID), "thumb.png")); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		// Review finding F2 (ut-docs#1189): this handler used to write ONLY
		// the disk file — the admin Catalog table (which checks the file)
		// showed the new photo, but the POS sale-screen grid/basket/self-
		// order/suggestions (which resolve via item_images/ImageURL) never
		// saw it, so a placeholder icon (or nothing) kept showing there
		// forever with no in-app way to clear it. Best-effort: the photo
		// is already saved and correct on disk either way, so a DB hiccup
		// here logs rather than fails the upload.
		if err := repo.SetItemThumbnail(r.Context(), itemID, "/public/assets/items/"+itemID+"/thumb.png"); err != nil {
			log.Printf("[catalog] record item_images thumbnail for %s: %v", itemID, err)
		}
		writeRowOOB(w, r, itemID, false)
	})

	// Built-in icon picker (ut-docs#1844): a bundled category icon
	// (catimport.BuiltinIcons — the same 5 assets PlaceholderIcon already
	// picks from for imageless imports) is a valid alternative to an
	// uploaded photo, stored through the identical item_images/thumbnail
	// row so every existing reader (POS grid, basket, self-order,
	// suggestions) needs no change to pick it up.
	//
	// icon-state tells the picker what to show when it opens for a given
	// item: which built-in key (if any) is currently selected, whether the
	// current thumbnail is instead a custom upload (a built-in choice and a
	// custom photo are mutually exclusive — picking one always replaces
	// the other, same as the existing upload-over-placeholder behaviour),
	// and which key PlaceholderIcon's own keyword match would suggest for
	// this item's actual name/category — so a still-imageless item shows a
	// sensible preselected tile instead of nothing.
	mux.HandleFunc("GET /api/catalog/item/icon-state", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagement(w, r) {
			return
		}
		itemID := strings.TrimSpace(r.URL.Query().Get("item_id"))
		if itemID == "" {
			writeJSON(w, http.StatusBadRequest, nil, "item_id required")
			return
		}
		itm, ok, err := repo.GetItem(r.Context(), itemID)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		if !ok {
			writeJSON(w, http.StatusNotFound, nil, "item not found")
			return
		}
		var categoryName string
		if itm.CategoryID != nil {
			if cat, err := repo.GetLookup(r.Context(), "categories", *itm.CategoryID); err == nil {
				categoryName = cat.Name
			}
		}
		suggestedKey := catimport.PlaceholderIcon(itm.Name, categoryName)
		selectedKey := ""
		isCustom := false
		if path, hasPath, err := repo.ItemThumbnailPath(r.Context(), itemID); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		} else if hasPath {
			isCustom = true
			for _, ic := range catimport.BuiltinIcons() {
				if ic.Path == path {
					selectedKey = ic.Key
					isCustom = false
					break
				}
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"selected_key":  selectedKey,
			"is_custom":     isCustom,
			"suggested_key": suggestedKey,
		}, "")
	})

	// Choosing (or clearing) a built-in icon. icon="" (or "none") clears
	// back to no thumbnail at all — distinct from picking "generic", which
	// is a real, visible choice. Deliberately NOT requirePrimary-gated,
	// same reasoning as item/image above: this only ever touches
	// item_images, which sync_admin_repo.go's adminTables explicitly
	// excludes (files/icon choices don't travel over the sync bundle).
	mux.HandleFunc("POST /api/catalog/item/icon", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagement(w, r) {
			return
		}
		_ = r.ParseForm()
		itemID := strings.TrimSpace(r.Form.Get("item_id"))
		// Review finding F1 (ut-docs#1844): this handler now also removes
		// an item's uploaded thumbnail file (see removeUploadedThumbnail
		// below), which makes item_id reach a filesystem path for the
		// first time on this route — so it needs the same path-traversal
		// guard the sibling upload handler already applies to itemID.
		if itemID == "" || strings.ContainsAny(itemID, "/\\.") {
			http.Error(w, "valid item_id required", http.StatusBadRequest)
			return
		}
		if ok, err := repo.ItemExists(r.Context(), itemID); err != nil || !ok {
			http.Error(w, "item not found", http.StatusNotFound)
			return
		}
		icon := strings.TrimSpace(r.Form.Get("icon"))
		if icon == "" || icon == "none" {
			if err := repo.ClearItemThumbnail(r.Context(), itemID); err != nil {
				common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
				return
			}
			removeUploadedThumbnail(itemID)
			writeRowOOB(w, r, itemID, false)
			return
		}
		path, ok := catimport.IconPath(icon)
		if !ok {
			http.Error(w, "unknown icon", http.StatusBadRequest)
			return
		}
		if err := repo.SetItemThumbnail(r.Context(), itemID, path); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		removeUploadedThumbnail(itemID)
		writeRowOOB(w, r, itemID, false)
	})

	// Variant image upload → assets/items/<itemID>/variants/<variantID>/thumb.png
	// (docs: architecture/variant-images.md). Fallback chain: variant → item →
	// placeholder, resolved by the template's imgv versioned URLs.
	//
	// Deliberately NOT requirePrimary-gated, same reasoning as item/image
	// above: this writes only image files (item_images), never
	// item_variants itself, so there is no synced row for a replica write
	// to lose on the next admin pull.
	mux.HandleFunc("POST /api/catalog/variant/image", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagement(w, r) {
			return
		}
		if err := r.ParseMultipartForm(10 << 20); err != nil {
			common.LocalizedError(w, r, http.StatusBadRequest, "common.error.invalid_upload")
			return
		}
		variantID := strings.TrimSpace(r.Form.Get("variant_id"))
		if variantID == "" || strings.ContainsAny(variantID, "/\\.") {
			http.Error(w, "valid variant_id required", http.StatusBadRequest)
			return
		}
		vl, ok, err := repo.GetVariantLabel(r.Context(), variantID)
		if err != nil || !ok {
			http.Error(w, "variant not found", http.StatusNotFound)
			return
		}
		itemID := strings.TrimSpace(r.Form.Get("panelItem"))
		if itemID == "" || strings.ContainsAny(itemID, "/\\.") {
			http.Error(w, "valid panelItem required", http.StatusBadRequest)
			return
		}
		_ = vl
		file, _, err := r.FormFile("image")
		if err != nil {
			http.Error(w, "image file required", http.StatusBadRequest)
			return
		}
		defer file.Close()
		raw, readErr := io.ReadAll(io.LimitReader(file, 10<<20))
		if readErr != nil {
			common.LocalizedError(w, r, http.StatusBadRequest, "catalog.error.image_invalid")
			return
		}
		img, err := imaging.PrepareThumb(raw)
		if err != nil {
			common.LocalizedError(w, r, http.StatusBadRequest, ThumbErrorKey(err))
			return
		}
		if err := imaging.WriteThumbPNG(img, filepath.Join(paths.Data("public", "assets", "items", itemID, "variants", variantID), "thumb.png")); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		renderVariantsPanel(w, r, itemID, false)
	})

	mux.HandleFunc("/api/catalog/barcode", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		code := strings.TrimSpace(r.Form.Get("barcode"))
		itemID := strings.TrimSpace(r.Form.Get("itemId"))
		variantID := strings.TrimSpace(r.Form.Get("variantId"))
		// The row whose summary must refresh afterwards is the item's even
		// when the barcode attaches to a variant — remember it before the
		// variant-wins clearing below.
		rowItemID := itemID
		// The form carries both (item picked from a row + optional variant). A
		// barcode attaches to exactly one — prefer the variant when chosen.
		if variantID != "" {
			itemID = ""
		}
		isPrimary := r.Form.Get("isPrimary") == "1" || strings.ToLower(r.Form.Get("isPrimary")) == "on"
		if code == "" {
			http.Error(w, "barcode required", http.StatusBadRequest)
			return
		}
		in := pos.BarcodeInput{
			Barcode:   code,
			ItemID:    itemID,
			VariantID: variantID,
			IsPrimary: isPrimary,
		}
		// ut-docs#948 F1: once a shop enables an embedded symbology
		// (EAN13_WEIGHT_PREFIX2X/EAN13_PRICE_PREFIX02, ADR-0059), the
		// untyped-inference path in AddBarcode would classify ANY
		// check-digit-valid EAN-13 in that prefix range as embedded-data
		// first — even a genuine plain retail product that happens to
		// share the prefix. Checking "plain code" here passes an explicit
		// BarcodeType, which takes AddBarcode's existing
		// explicit-type-bypasses-inference path (ADR-0059 §3) instead —
		// but only for a genuine EAN-13 (see plainBarcodeTypeFor / F-2).
		in.BarcodeType = plainBarcodeTypeFor(r, code)
		if err := pos.AddBarcode(r.Context(), d.Db, in); err != nil {
			locale := httpx.ResolveLocale(w, r)
			if errors.Is(err, data.ErrInvalidEAN13) {
				http.Error(w, httpx.T(locale, "catalog.error.invalid_ean13"), http.StatusBadRequest)
				return
			}
			// Same fix as the auto-fill flow above (ut-docs#303): name the
			// conflicting item/variant instead of leaking its raw ID.
			http.Error(w, common.FriendlyBarcodeConflict(r.Context(), repo, locale, err), http.StatusBadRequest)
			return
		}
		if panelItem := strings.TrimSpace(r.Form.Get("panelItem")); panelItem != "" {
			renderVariantsPanel(w, r, panelItem, true)
			return
		}
		// No panel open (the keypad-mapper path): answer with the affected
		// item's row. A variant-only submission resolves its parent item.
		if rowItemID == "" && variantID != "" {
			if id, ok, err := repo.ItemIDForVariant(r.Context(), variantID); err == nil && ok {
				rowItemID = id
			} else if err != nil {
				log.Printf("[catalog] resolve item for variant %s: %v", variantID, err)
			}
		}
		if rowItemID != "" {
			writeRowOOB(w, r, rowItemID, false)
		}
	})

	// Detach a barcode (mis-scans and reassignments are routine corrections).
	mux.HandleFunc("POST /api/catalog/barcode/delete", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		_ = r.ParseForm()
		barcode := strings.TrimSpace(r.Form.Get("barcode"))
		if barcode == "" {
			http.Error(w, "barcode required", http.StatusBadRequest)
			return
		}
		// Resolve the owning item BEFORE the delete — afterwards the code
		// resolves to nothing. Best-effort: the delete must not be blocked
		// by this read failing.
		ownerID, ownerOK, err := repo.ItemIDForBarcode(r.Context(), barcode)
		if err != nil {
			log.Printf("[catalog] resolve item for barcode %s: %v", barcode, err)
		}
		if err := pos.RemoveBarcode(r.Context(), d.Db, barcode); err != nil {
			common.LogAndLocalizedError(w, r, http.StatusBadRequest, "catalog.error.invalid_request", "catalog", err)
			return
		}
		if panelItem := strings.TrimSpace(r.Form.Get("panelItem")); panelItem != "" {
			renderVariantsPanel(w, r, panelItem, true)
			return
		}
		if ownerOK {
			writeRowOOB(w, r, ownerID, false)
		}
	})

	// ut-docs#1356: bulk "backfill barcodes from SKU" — preview (dry run,
	// GET, no writes) + commit (POST). Both share computeBarcodeBackfillPlan
	// below, which reuses ut-docs#1224's exported derivation
	// (catimport.DeriveNumberBarcode, ADR-0059 §3 "call the same function,
	// don't reimplement it") and the dry-run-safe BarcodeOwner lookup —
	// never ensureBarcodeAvailable/AddBarcode's own transactional check,
	// which stays load-bearing for the real write path (see AddBarcode
	// below and BarcodeOwner's own doc comment).
	mux.HandleFunc("GET /api/catalog/barcode-backfill", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagement(w, r) {
			return
		}
		locale := httpx.ResolveLocale(w, r)
		funcs := httpx.FuncsFor(locale)
		T := funcs["T"].(func(string) string)
		settingsRepo := data.NewSettingsRepo(d.Db)
		enabledIDs, symErr := settingsRepo.EnabledBarcodeSymbologies(r.Context())
		if symErr != nil {
			log.Printf("[catalog] enabled symbologies unavailable for barcode backfill, using defaults: %v", symErr)
		}
		assign, noSymbology, alreadyUsed, err := computeBarcodeBackfillPlan(r.Context(), repo, enabledIDs, locale, T)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		httpx.RenderWith(files(
			filepath.Join("web", "ui", "partials", "catalog_barcode_backfill.html"),
		), funcs)("catalog_barcode_backfill", barcodeBackfillPreviewView(assign, noSymbology, alreadyUsed))(w, r)
	})

	mux.HandleFunc("POST /api/catalog/barcode-backfill", func(w http.ResponseWriter, r *http.Request) {
		if !requireCatalogManagement(w, r) {
			return
		}
		if !requirePrimary(w, r, "catalog.error.item_replica_use_primary") {
			return
		}
		locale := httpx.ResolveLocale(w, r)
		funcs := httpx.FuncsFor(locale)
		settingsRepo := data.NewSettingsRepo(d.Db)
		enabledIDs, symErr := settingsRepo.EnabledBarcodeSymbologies(r.Context())
		if symErr != nil {
			log.Printf("[catalog] enabled symbologies unavailable for barcode backfill, using defaults: %v", symErr)
		}
		// Re-derive eligibility fresh against CURRENT data — never trust the
		// preview (ut-docs#1356): a concurrent edit between preview and
		// commit (another operator adding a barcode, an item going
		// inactive, …) must not misfire.
		T := funcs["T"].(func(string) string)
		assign, noSymbology, alreadyUsed, err := computeBarcodeBackfillPlan(r.Context(), repo, enabledIDs, locale, T)
		if err != nil {
			common.LogAndLocalizedError(w, r, http.StatusInternalServerError, "catalog.error.server", "catalog", err)
			return
		}
		var assigned int
		// Pre-seeded with the plan's own two issue buckets (items never
		// attempted at all — not eligible even before this commit ran) so
		// the result names EVERY item that didn't get a barcode and why,
		// not only the rarer AddBarcode-time race case the loop below adds
		// to it.
		skipped := make([]backfillSkip, 0, len(noSymbology)+len(alreadyUsed))
		skipped = append(skipped, noSymbology...)
		skipped = append(skipped, alreadyUsed...)
		for _, row := range assign {
			if aerr := pos.AddBarcode(r.Context(), d.Db, pos.BarcodeInput{
				Barcode: row.Barcode, BarcodeType: row.BarcodeType, ItemID: row.ItemID, IsPrimary: true,
			}); aerr != nil {
				// AddBarcode re-checks availability inside its own IMMEDIATE
				// transaction (ut-docs#304) — a race since the plan above
				// was computed (another request/import claiming the same
				// derived code first) is an ordinary "skipped: already in
				// use" outcome here, not a batch-aborting error.
				skipped = append(skipped, backfillSkip{
					Name: row.Name, SKU: row.SKU,
					Reason: common.FriendlyBarcodeConflict(r.Context(), repo, locale, aerr),
				})
				continue
			}
			assigned++
		}
		// Deliberately NOT HX-Refresh (unlike pairing_api.go/backup_api.go's
		// own bulk endpoints): htmx processes that header before the swap,
		// so it would reload the page and discard this very result fragment
		// before the operator ever saw it — a partial backfill (some SKUs
		// skipped) would then look identical to a full one. The result
		// fragment's own "Close" button (catalog_barcode_backfill.html)
		// reloads instead, once the operator has actually read the report —
		// same close-then-reload shape as plugin_install_modal.html.
		httpx.RenderWith(files(
			filepath.Join("web", "ui", "partials", "catalog_barcode_backfill.html"),
		), funcs)("catalog_barcode_backfill_result", barcodeBackfillResultView(assigned, skipped))(w, r)
	})
}

// backfillRow is one item computeBarcodeBackfillPlan found eligible for a
// SKU-derived barcode (ut-docs#1356).
type backfillRow struct {
	ItemID      string
	Name        string
	SKU         string
	Barcode     string
	BarcodeType string
}

// backfillSkip is one item computeBarcodeBackfillPlan (or the commit loop)
// could NOT assign a barcode to, with an operator-facing, already-
// translated reason.
type backfillSkip struct {
	Name   string
	SKU    string
	Reason string
}

// backfillPreviewRowCap caps the preview's eligible-list display — same
// "don't render an unbounded table" convention as import_page.go's own
// 200-row cap, sized smaller here since this renders inside a dialog, not a
// full page (ut-docs#1356 brief: "first ~50").
const backfillPreviewRowCap = 50

// computeBarcodeBackfillPlan re-derives, from CURRENT data, which of
// CatalogRepo.ItemsWithoutBarcode's candidates are actually eligible for a
// SKU-derived barcode right now — the shared logic both the preview (dry
// run) and the commit call, so a concurrent edit between them can never
// leave the commit trusting stale eligibility. For each candidate:
//   - DeriveNumberBarcode reports an issue (no enabled symbology matches the
//     SKU's shape) → the "can't derive" bucket.
//   - Otherwise BarcodeOwner finds the derived code already claimed by a
//     different item/variant → the "already in use" bucket, named via
//     FriendlyBarcodeConflict (ut-docs#303) — reused by constructing the
//     exact *data.BarcodeConflictError AddBarcode itself would return,
//     rather than duplicating its item/variant label resolution.
//   - Otherwise → eligible to assign.
func computeBarcodeBackfillPlan(ctx context.Context, repo *data.CatalogRepo, enabledIDs []string, locale string, T func(string) string) (assign []backfillRow, noSymbology, alreadyUsed []backfillSkip, err error) {
	items, ierr := repo.ItemsWithoutBarcode(ctx)
	if ierr != nil {
		return nil, nil, nil, fmt.Errorf("barcode backfill: %w", ierr)
	}
	// Two DIFFERENT SKUs in this same candidate set can derive the SAME
	// code — DeriveNumberBarcode's underlying LookupKey collapses some
	// distinct-looking numbers onto one key (e.g. EAN13 weight/price-prefix
	// variants), and normalizeBarcode strips a trailing ".0". items.sku is
	// UNIQUE, so both rows are legitimate candidates, but only the first can
	// actually get the code — BarcodeOwner only sees data already committed
	// before this plan ran, so without tracking this batch's own claims a
	// later duplicate would show as "eligible" in the preview and then
	// silently fail to assign at commit time. Track them here so the
	// preview's count and the commit's actual result always agree.
	claimedInBatch := make(map[string]string) // code -> name of the item that claimed it first
	for _, it := range items {
		code, codeType, issue, _ := catimport.DeriveNumberBarcode(it.SKU, enabledIDs)
		if issue != "" || code == "" {
			noSymbology = append(noSymbology, backfillSkip{Name: it.Name, SKU: it.SKU, Reason: T("catalog.barcode_backfill.issue.no_symbology")})
			continue
		}
		if claimant, dup := claimedInBatch[code]; dup {
			reason := fmt.Sprintf(T("catalog.error.barcode_conflict"), claimant)
			alreadyUsed = append(alreadyUsed, backfillSkip{Name: it.Name, SKU: it.SKU, Reason: reason})
			continue
		}
		targetType, targetID, found, oerr := repo.BarcodeOwner(ctx, code)
		if oerr != nil {
			return nil, nil, nil, fmt.Errorf("barcode backfill: %w", oerr)
		}
		if found {
			// The candidate item itself can never be this owner:
			// ItemsWithoutBarcode already excludes any item that has a row
			// in item_barcodes, so a found owner here is always a
			// DIFFERENT item or variant.
			reason := common.FriendlyBarcodeConflict(ctx, repo, locale, &data.BarcodeConflictError{TargetType: targetType, TargetID: targetID})
			alreadyUsed = append(alreadyUsed, backfillSkip{Name: it.Name, SKU: it.SKU, Reason: reason})
			continue
		}
		claimedInBatch[code] = it.Name
		assign = append(assign, backfillRow{ItemID: it.ID, Name: it.Name, SKU: it.SKU, Barcode: code, BarcodeType: codeType})
	}
	return assign, noSymbology, alreadyUsed, nil
}

// barcodeBackfillPreviewView shapes computeBarcodeBackfillPlan's result for
// catalog_barcode_backfill.html, capping the eligible list at
// backfillPreviewRowCap.
func barcodeBackfillPreviewView(assign []backfillRow, noSymbology, alreadyUsed []backfillSkip) map[string]any {
	shown := assign
	moreCount := 0
	if len(assign) > backfillPreviewRowCap {
		shown = assign[:backfillPreviewRowCap]
		moreCount = len(assign) - backfillPreviewRowCap
	}
	return map[string]any{
		"Eligible":      shown,
		"EligibleCount": len(assign),
		"MoreCount":     moreCount,
		"NoSymbology":   noSymbology,
		"AlreadyUsed":   alreadyUsed,
		"Empty":         len(assign) == 0 && len(noSymbology) == 0 && len(alreadyUsed) == 0,
	}
}

// barcodeBackfillResultView shapes the commit loop's outcome for
// catalog_barcode_backfill.html's result fragment.
func barcodeBackfillResultView(assigned int, skipped []backfillSkip) map[string]any {
	return map[string]any{
		"Assigned": assigned,
		"Skipped":  skipped,
	}
}

// saveLookupImage downloads an allowlisted product-database image and stores
// it as the item's thumb.png (same convention as the manual upload path).
// Best-effort: the item is fine without it.
func saveLookupImage(ctx context.Context, c *productlookup.Client, itemID, imgURL string) error {
	raw, err := c.FetchImage(ctx, imgURL)
	if err != nil {
		return err
	}
	img, err := imaging.Decode(raw)
	if err != nil {
		return err
	}
	img = imaging.DownscaleMaxEdge(img, imaging.MaxThumbEdge)
	dir := paths.Data("public", "assets", "items", itemID)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	out, err := os.Create(filepath.Join(dir, "thumb.png"))
	if err != nil {
		return err
	}
	defer out.Close()
	return png.Encode(out, img)
}

// ThumbErrorKey maps an imaging.PrepareThumb error to the operator-facing
// locale key: a pixel bomb / oversized photo gets its own "too large"
// message, anything else is "not a valid PNG/JPEG" (ut-docs#1416). Shared
// with the category image upload (ut-docs#2500) so both say the same thing.
func ThumbErrorKey(err error) string {
	if errors.Is(err, imaging.ErrTooManyPixels) {
		return "catalog.error.image_too_large"
	}
	return "catalog.error.image_invalid"
}

// removeUploadedThumbnail deletes an item's uploaded thumbnail file, if one
// exists, at the same "public/assets/items/<id>/thumb.png" path the upload
// handler above writes to. This is the built-in icon picker's (ut-docs#1844)
// fix for review finding F1: three surfaces resolve an item's photo by this
// path CONVENTION rather than through item_images — catalog_row.html and
// catalog_variants.html's `imgExists` check, and self_order_shop.go's
// hardcoded ImageURL (that file's own comment already documents this same
// convention-vs-item_images split, ut-docs#1189). Without this, choosing a
// built-in icon (or clearing back to none) updates item_images but leaves
// the old uploaded photo file in place, so those three surfaces keep
// showing the superseded photo while every item_images-driven surface (POS
// grid, basket, search, suggestions) correctly shows the new choice —
// visibly inconsistent. Best-effort: the DB write (the record of what the
// operator actually chose) already succeeded by the time this runs, so a
// stray leftover file on disk is a cosmetic follow-up, not a reason to fail
// the request the operator is waiting on. itemID is validated by the caller
// (no "/", "\" or "." — see the path-traversal guard on the /icon route)
// before it ever reaches this function.
func removeUploadedThumbnail(itemID string) {
	path := filepath.Join(paths.Data("public", "assets", "items", itemID), "thumb.png")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		log.Printf("[catalog] remove superseded upload for %s: %v", itemID, err)
	}
}

// bufResponseWriter captures a partial render into a buffer so it can be
// post-processed (the out-of-band table fragment).
type bufResponseWriter struct {
	buf    *bytes.Buffer
	header http.Header
}

func newBufResponseWriter(buf *bytes.Buffer) *bufResponseWriter {
	return &bufResponseWriter{buf: buf, header: http.Header{}}
}

func (b *bufResponseWriter) Header() http.Header         { return b.header }
func (b *bufResponseWriter) Write(p []byte) (int, error) { return b.buf.Write(p) }
func (b *bufResponseWriter) WriteHeader(int)             {}

func strPtr(s string) *string {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return &s
}

// formCheckboxActive reads a checkbox paired with a hidden isActive=0
// fallback (an unchecked box submits nothing on its own). Checked ⇒ the
// browser sends BOTH the hidden "0" and the checkbox's "1", in DOM order —
// hidden-then-checkbox means Form.Get alone would always see "0" first and
// read as inactive even when checked, so this scans every submitted value
// for a "1" rather than trusting the first one.
func formCheckboxActive(r *http.Request) bool {
	vals := r.Form["isActive"]
	if len(vals) == 0 {
		return true // no isActive field at all: caller didn't use the hidden-fallback pattern, default active
	}
	for _, v := range vals {
		if v == "1" {
			return true
		}
	}
	return false
}

// plainBarcodeTypeFor returns "EAN13" when the operator checked the "plain
// code" escape hatch (ut-docs#948 F1) AND code is actually a valid EAN-13,
// else "" (leave AddBarcode's untyped inference to run).
//
// The EAN-13 guard is the F-2 review fix: forcing BarcodeType:"EAN13"
// makes AddBarcode assert a valid EAN-13 check digit, so blindly forcing
// it would reject an operator who ticks the box on a perfectly valid EAN-8
// / UPC-A / GTIN-14 / CODE128 / internal-PLU code. That rejection would
// also be gratuitous: the only symbologies the escape hatch exists to
// override — EAN13_WEIGHT_PREFIX2X / EAN13_PRICE_PREFIX02 — both require a
// valid EAN-13 check digit to match at all (ADR-0059 §1), so a non-EAN-13
// code can never be mis-inferred as embedded-data in the first place and
// needs no escaping. Ticking the box on such a code is therefore a no-op:
// untyped inference already picks a plain symbology for it.
func plainBarcodeTypeFor(r *http.Request, code string) string {
	v := r.Form.Get("forcePlainBarcode")
	checked := v == "1" || strings.ToLower(v) == "on"
	if checked && barcode.ValidEAN13Checksum(code) {
		return "EAN13"
	}
	return ""
}

func parseItemInput(r *http.Request) (pos.ItemInput, error) {
	name := strings.TrimSpace(r.Form.Get("name"))
	priceStr := strings.TrimSpace(r.Form.Get("price"))
	if name == "" || priceStr == "" {
		return pos.ItemInput{}, errors.New("name and price required")
	}
	price, err := strconv.ParseInt(priceStr, 10, 64)
	if err != nil {
		return pos.ItemInput{}, errors.New("invalid price")
	}
	cat := strPtr(r.Form.Get("categoryId"))
	brand := strPtr(r.Form.Get("brandId"))
	taxCode := strPtr(r.Form.Get("taxCode"))
	return pos.ItemInput{
		Name:        name,
		SKU:         strings.TrimSpace(r.Form.Get("sku")),
		BasePrice:   price,
		TaxCodeID:   taxCode,
		CategoryID:  cat,
		BrandID:     brand,
		Description: strings.TrimSpace(r.Form.Get("description")),
		Unit:        strings.TrimSpace(r.Form.Get("unit")),
		// ut-docs#1901: "" is a real, valid value here (the "no color"
		// swatch tile) — validated against the fixed palette allowlist by
		// validateLookups below, same convention as category/brand/tax.
		Color:     strings.TrimSpace(r.Form.Get("color")),
		IsWeighed: r.Form.Get("isWeighed") == "1" || strings.ToLower(r.Form.Get("isWeighed")) == "on",
		// ut-docs#1850: unchecked (missing from the form) correctly reads
		// as false/tracked — no hidden-fallback trick needed, unlike
		// isActive below (that one defaults CHECKED, this one doesn't).
		StockUntracked: r.Form.Get("stockUntracked") == "1" || strings.ToLower(r.Form.Get("stockUntracked")) == "on",
		// formCheckboxActive, not a bare Form.Get: the item form now pairs
		// its Active checkbox with a hidden isActive=0 fallback
		// (ut-docs#1367), same convention as the variant/modifier-group
		// forms — see that helper's own comment for why Form.Get alone
		// would get this backwards.
		IsActive: formCheckboxActive(r),
	}, nil
}

type lookup struct {
	ID   string
	Name string
}

func listLookups(ctx context.Context, repo *data.CatalogRepo) ([]lookup, []lookup, error) {
	catsRaw, err := repo.ReadLookup(ctx, "categories")
	if err != nil {
		return nil, nil, err
	}
	brandsRaw, err := repo.ReadLookup(ctx, "brands")
	if err != nil {
		return nil, nil, err
	}
	return convertLookups(catsRaw), convertLookups(brandsRaw), nil
}

func convertLookups(in []data.Lookup) []lookup {
	var out []lookup
	for _, l := range in {
		out = append(out, lookup{ID: l.ID, Name: l.Name})
	}
	return out
}

// categoryFilterNodeJSON is one data.CategoryNode as the category-filter
// chip row's client-side JS needs it (ut-docs#2119): id/name/parentId only
// — category_filter.html renders the chips themselves server-side (one per
// TOP-LEVEL category), but category-filter.js's expand() still needs the
// FULL flat tree, children included, to walk parent-includes-children at
// match time. camelCase tags on purpose: this is a page-embedded JS blob
// (like inventory_page.go's pickerItem/ItemsJSON), not a JSON API response,
// so the repo-wide snake_case wire convention doesn't apply here.
type categoryFilterNodeJSON struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	ParentID string `json:"parentId"`
}

// categoryFilterNodesJSON serializes the full flat category list (the same
// slice CategoryFilterOptions renders chips from) for category-filter.js's
// expand() tree walk — see that field's own comment on why both a Go-side
// and a JS-side view of the same data are needed.
func categoryFilterNodesJSON(nodes []data.CategoryNode) template.JS {
	out := make([]categoryFilterNodeJSON, 0, len(nodes))
	for _, n := range nodes {
		out = append(out, categoryFilterNodeJSON{ID: n.ID, Name: n.Name, ParentID: n.ParentID})
	}
	b, err := json.Marshal(out)
	if err != nil {
		return template.JS("[]")
	}
	return template.JS(b)
}

func validateLookups(ctx context.Context, repo *data.CatalogRepo, in pos.ItemInput) error {
	if in.CategoryID != nil {
		if err := repo.ValidateLookup(ctx, "categories", *in.CategoryID, false); err != nil {
			return err
		}
	}
	if in.BrandID != nil {
		if err := repo.ValidateLookup(ctx, "brands", *in.BrandID, false); err != nil {
			return err
		}
	}
	if in.TaxCodeID != nil {
		if err := repo.ValidateLookup(ctx, "tax_codes", *in.TaxCodeID, false); err != nil {
			return err
		}
	}
	// ut-docs#1901: color has no lookup table (it's a fixed, curated
	// palette, not an admin-editable list), so this checks it against
	// catalogtypes.ItemColors() directly rather than calling
	// repo.ValidateLookup — same "clean, bounded, hand-written" error
	// convention the caller's own comment already documents for this
	// function's other checks (never raw SQL/driver text), since a raw,
	// unvalidated value here would otherwise reach a CSS custom property.
	if !catalogtypes.ValidItemColor(in.Color) {
		return errors.New("invalid color")
	}
	return nil
}
func files(paths ...string) []string { return paths }

// skuAwareError handles a Create/UpdateItem or Create/UpdateVariant error:
// a duplicate SKU (data.ErrSKUExists) is common enough, and specific
// enough to name, that downgrading it to the generic
// "catalog.error.invalid_request" (ut-docs#316's review) throws away
// actionable feedback — same reasoning ut-docs#303 already applied to
// barcode conflicts via FriendlyBarcodeConflict. Anything else still goes
// through the generic translated+logged path.
func skuAwareError(w http.ResponseWriter, r *http.Request, status int, err error) {
	if errors.Is(err, data.ErrSKUExists) {
		common.LocalizedError(w, r, http.StatusBadRequest, "catalog.error.sku_exists")
		return
	}
	common.LogAndLocalizedError(w, r, status, "catalog.error.invalid_request", "catalog", err)
}

// optionSetAwareError is skuAwareError's twin for the option-set routes
// (ut-docs#1900): a duplicate set name or a duplicate value within a set is
// the one mistake an operator actually makes here, so it gets its own
// actionable message; anything else takes the generic translated+logged path.
func optionSetAwareError(w http.ResponseWriter, r *http.Request, status int, err error) {
	if errors.Is(err, data.ErrOptionSetExists) || errors.Is(err, data.ErrOptionSetValueExists) {
		common.LocalizedError(w, r, status, "catalog.option_sets.exists")
		return
	}
	common.LogAndLocalizedError(w, r, status, "catalog.error.invalid_request", "catalog", err)
}

// itemStationCheck is one kitchen-station checkbox on the item editor's
// routing section (ut-docs#2284): Checked when the item's OWN override
// routes to it. Every station is listed, a disabled one labelled, for the
// same reason categories.html lists them all — an override pointing at a
// since-disabled station must keep its tick rather than silently lose the
// route on the next save.
type itemStationCheck struct {
	ID      string
	Name    string
	Checked bool
	Enabled bool
}

// itemRoutingData assembles the Variants panel's routing section
// (ut-docs#2284): the station checkboxes (ticked from item_station_routes),
// the item's category's own routes as a display string, and whether an
// item-level override exists at all. Every read is best-effort — a repo
// error (including the hand-rolled catalog test schema having no station
// tables) yields "no stations", and the template then renders no routing
// section rather than failing the whole panel.
func itemRoutingData(ctx context.Context, repo *data.CatalogRepo, posRepo *data.POSRepo, itemID string) ([]itemStationCheck, string, bool) {
	stations, err := posRepo.ListKitchenStations(ctx)
	if err != nil || len(stations) == 0 {
		return nil, "", false
	}
	byID := make(map[string]string, len(stations))
	for _, s := range stations {
		byID[s.ID] = s.Name
	}
	itemRoutes, _ := posRepo.ItemStationRoutes(ctx, itemID)
	routed := make(map[string]bool, len(itemRoutes))
	for _, id := range itemRoutes {
		routed[id] = true
	}
	checks := make([]itemStationCheck, 0, len(stations))
	for _, s := range stations {
		checks = append(checks, itemStationCheck{ID: s.ID, Name: s.Name, Checked: routed[s.ID], Enabled: s.Enabled})
	}
	var categoryNames []string
	if item, ok, err := repo.GetItem(ctx, itemID); err == nil && ok && item.CategoryID != nil && *item.CategoryID != "" {
		catRoutes, _ := posRepo.CategoryStationRoutes(ctx, *item.CategoryID)
		for _, id := range catRoutes {
			if name, ok := byID[id]; ok {
				categoryNames = append(categoryNames, name)
			}
		}
	}
	return checks, strings.Join(categoryNames, ", "), len(itemRoutes) > 0
}
