package ui

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"hash/fnv"
	"html"
	"html/template"
	"net/http"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	pos "github.com/universaltill/universal-till/internal/pos"
)

// designerErrorServerKey mirrors internal/pages/buttons_api.go's
// buttonsErrorKey ("designer.error.server"). internal/pages/common imports
// internal/ui (common.Deps.BtnStore is a *ButtonStore), so internal/ui
// importing back into internal/pages/common (where
// common.LogAndLocalizedError lives) would be an import cycle -- this key
// is duplicated here rather than shared. Keep both literals in sync if the
// key ever changes; both are covered by guard-i18n.sh either way since it
// scans web/locales, not these Go constants.
const designerErrorServerKey = "designer.error.server"

// Button represents a shortcut button backed by the shortcut_buttons table.
type Button struct {
	Label        string `json:"label"`
	Code         string `json:"code"` // barcode/PLU associated with the shortcut
	ItemID       string `json:"itemId"`
	ImageURL     string `json:"imageUrl,omitempty"`
	Price        int64  `json:"price,omitempty"` // minor units, display only
	HasModifiers bool   `json:"hasModifiers,omitempty"`
	// HasVariants (ut-docs#2209) mirrors HasModifiers: the item has at
	// least one active item_variants row (small/regular/large sizing, …),
	// so tapping the tile must open the picker to ask which variant
	// instead of adding the parent item's own base price straight to the
	// basket.
	HasVariants bool   `json:"hasVariants,omitempty"`
	CategoryID  string `json:"categoryId,omitempty"` // the item's category, empty when uncategorized
	// Color is the item's tile swatch (ut-docs#1901) — empty when unset.
	Color string `json:"color,omitempty"`
}

// ButtonVM is the view-model passed to templates.
type ButtonVM struct {
	Label        string `json:"label"`
	Code         string `json:"code"`
	ItemID       string `json:"itemId"`
	ImageURL     string `json:"imageUrl,omitempty"`
	Price        int64  `json:"price,omitempty"` // minor units, display only
	HasModifiers bool   `json:"hasModifiers,omitempty"`
	// HasVariants — see Button.HasVariants; product-tile (buttons.html)
	// ORs it with HasModifiers to decide whether tapping the tile opens
	// the picker or adds straight to the basket.
	HasVariants bool `json:"hasVariants,omitempty"`
	// Color is the item's tile swatch (ut-docs#1901) — product-tile
	// (buttons.html) renders it as the tile's solid background, via
	// --tile-color, ONLY when the tile has no ImageURL: a real photo
	// always wins.
	Color string `json:"color,omitempty"`
	// Pos (ut-docs#2339) is this button's index in the GLOBAL sort_order
	// list (the order Load returns) — set by BuildCategoryGroups only, so
	// it's 0 (meaningless) on a ButtonVM built via ToVM for the Designer's
	// flat admin grid, where the slice index already IS the global index.
	// The sale-screen grid groups tiles by category, so its DOM order
	// stops being the global order the moment categories interleave;
	// product-tile (buttons.html) renders this as data-pos, and app.js's
	// utTileJiggle uses it to rebuild the full global list it POSTs to
	// /api/buttons/reorder after a drag: the reordered group's tiles are
	// re-dealt into the slots that same group already occupied, so a drag
	// within one category never moves another category's buttons in the
	// Designer's flat list — the same "nearest same-category neighbour"
	// outcome the retired ut-docs#2285 sheet's server-side Move produced,
	// computed client-side from these indices instead.
	Pos int `json:"pos"`
	// Locked (ut-docs#2361) mirrors tile_sheet.html's own .Locked field
	// (the retired ut-docs#2285 sheet, ut-docs#2312's review) -- true when
	// THIS SESSION does NOT hold catalog_management, the same permission
	// /api/buttons/{add,remove,reorder} and /catalog itself gate on. It's
	// a per-REQUEST value, identical across every tile in one render (the
	// permission isn't per-item), stamped on by List via stampLocked
	// rather than threaded through Button/toButtonVM -- there is no
	// per-button source for it to come from. product-tile (buttons.html)
	// uses it to show the same lock affordance tile_sheet.html already
	// established on the jiggle-mode edit/remove badges, before a cashier
	// attempts the action and hits the real server-side gate's elevation
	// prompt -- the discoverability affordance the retired sheet had and
	// the pure-CSS-toggle jiggle mode (ut-docs#2339) never carried
	// forward. Visual only: the badges stay fully clickable either way.
	Locked bool `json:"-"`
}

func ToVM(b []Button) []ButtonVM {
	out := make([]ButtonVM, 0, len(b))
	for _, x := range b {
		out = append(out, toButtonVM(x))
	}
	return out
}

func toButtonVM(x Button) ButtonVM {
	return ButtonVM{
		Label:        x.Label,
		Code:         x.Code,
		ItemID:       x.ItemID,
		ImageURL:     x.ImageURL,
		Price:        x.Price,
		HasModifiers: x.HasModifiers,
		HasVariants:  x.HasVariants,
		Color:        x.Color,
	}
}

// CategoryGroup is one node of the nested, color-coded sale-screen category
// tree — a category's own buttons plus its (already-pruned) subcategories.
// The synthetic "uncategorized" bucket (buttons whose item has no category,
// or whose category_id no longer resolves) has an empty ID and no Children;
// callers/templates tell it apart from a real category by ID == "".
type CategoryGroup struct {
	ID       string
	Name     string
	Color    string
	Buttons  []ButtonVM
	Children []*CategoryGroup

	// AncestorName is the top-level root's Name for a NESTED subcategory —
	// empty for a root itself (a root has no ancestor to disambiguate
	// against). ut-docs#2198: two subcategories sharing a name under
	// different top-level categories (e.g. Food>Specials and
	// Household>Specials) are indistinguishable once both are visible at
	// once (a cross-category search, or the default "All" tab), so the
	// template prefixes a nested header with this field while that
	// ambiguity is possible. Set once, at build time, to the root's name
	// only (not the immediate parent's) even for a grandchild — "at least
	// the top-level category name" is what the card asks for, and a
	// shallow label avoids a second breadcrumb-truncation problem this
	// card never scoped.
	AncestorName string
}

var hexColorRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// categoryPalette is a fixed set of readable, distinct accent colors used
// to auto-color-code a category that has no explicit color set — chosen so
// every till gets a color-coded grid out of the box, no admin configuration
// required, and the same category always lands on the same swatch.
var categoryPalette = []string{
	"#2563EB", "#DC2626", "#059669", "#D97706",
	"#7C3AED", "#DB2777", "#0891B2", "#65A30D",
}

// uncategorizedColor is the fixed neutral swatch for the synthetic
// "uncategorized" bucket — deliberately outside categoryPalette so it never
// visually collides with (and is never mistaken for) a real category.
const uncategorizedColor = "#64748B"

// resolveCategoryColor returns a category's explicit color if it's a valid
// #RRGGBB hex value, else a deterministic per-ID color from categoryPalette
// so the grid is color-coded even before any admin ever sets a color.
func resolveCategoryColor(c data.CategoryNode) string {
	if hexColorRE.MatchString(c.Color) {
		return c.Color
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(c.ID))
	return categoryPalette[h.Sum32()%uint32(len(categoryPalette))]
}

// BuildCategoryGroups nests buttons under their item's category (following
// each category's ParentID to build the tree cats itself doesn't carry
// nesting for) — "deep category trees, not a flat product list." Branches
// with no buttons anywhere in their subtree are pruned so an unused
// imported category never shows as an empty header on the till. Buttons
// with no category, or a category_id that no longer resolves, land in a
// trailing synthetic bucket (ID == ""), included only when non-empty.
func BuildCategoryGroups(buttons []Button, cats []data.CategoryNode) []*CategoryGroup {
	return buildCategoryGroups(buttons, cats, false)
}

// BuildCategoryGroupsKeepEmpty (ut-docs#2174) is BuildCategoryGroups with
// the empty-branch pruning switched off — for the Designer's live replica
// of the sale screen, which is an EDITOR: a category with no quick buttons
// yet (just created from the replica's own + tab, or emptied by removing
// its last button) must still render as a tab there, or the operator could
// never see, rename, recolour or fill the category they just made. The
// sale screen keeps pruning (an unused imported category never shows as an
// empty header on the till). Same tree, same order, same uncategorized
// bucket, same global Pos stamping — only the prune step differs.
func BuildCategoryGroupsKeepEmpty(buttons []Button, cats []data.CategoryNode) []*CategoryGroup {
	return buildCategoryGroups(buttons, cats, true)
}

func buildCategoryGroups(buttons []Button, cats []data.CategoryNode, keepEmpty bool) []*CategoryGroup {
	byID := make(map[string]*CategoryGroup, len(cats))
	nodeByID := make(map[string]data.CategoryNode, len(cats))
	for _, c := range cats {
		byID[c.ID] = &CategoryGroup{ID: c.ID, Name: c.Name, Color: resolveCategoryColor(c)}
		nodeByID[c.ID] = c
	}

	var roots []*CategoryGroup
	for _, c := range cats {
		g := byID[c.ID]
		if c.ParentID != "" && c.ParentID != c.ID {
			// A category whose ParentID chain loops back to itself (a
			// malformed import/edit) must never be attached as a child of
			// its own descendant: since it would then only be reachable
			// from within its own cycle, never from a real root, every
			// button in it would silently vanish from the grid instead of
			// just failing to nest as deep as configured. Treat it as a
			// root instead — buttons stay visible, the cycle is broken.
			if parent, ok := byID[c.ParentID]; ok && !isCategoryAncestor(c.ID, c.ParentID, nodeByID) {
				parent.Children = append(parent.Children, g)
				continue
			}
		}
		roots = append(roots, g)
	}

	var uncategorized []ButtonVM
	for i, b := range buttons {
		vm := toButtonVM(b)
		vm.Pos = i // global sort index — see ButtonVM.Pos
		g, ok := byID[b.CategoryID]
		if b.CategoryID == "" || !ok {
			uncategorized = append(uncategorized, vm)
			continue
		}
		g.Buttons = append(g.Buttons, vm)
	}

	if !keepEmpty {
		kept := roots[:0]
		for _, g := range roots {
			if pruneEmptyCategoryGroup(g) {
				kept = append(kept, g)
			}
		}
		roots = kept
	}

	for _, g := range roots {
		setAncestorNames(g, g.Name)
	}

	if len(uncategorized) > 0 {
		roots = append(roots, &CategoryGroup{Color: uncategorizedColor, Buttons: uncategorized})
	}
	return roots
}

// isCategoryAncestor reports whether id is an ancestor of candidateID,
// walking candidateID's ParentID chain upward. A local seen-set bounds the
// walk even if the data has a cycle not involving id itself, so this
// always terminates instead of looping on already-malformed input.
func isCategoryAncestor(id, candidateID string, nodes map[string]data.CategoryNode) bool {
	seen := map[string]bool{}
	cur := candidateID
	for cur != "" {
		if cur == id {
			return true
		}
		if seen[cur] {
			return false
		}
		seen[cur] = true
		n, ok := nodes[cur]
		if !ok {
			return false
		}
		cur = n.ParentID
	}
	return false
}

// setAncestorNames labels every descendant of g (recursively, all depths)
// with ancestor — g itself is left untouched, since g is always a root when
// called from BuildCategoryGroups and a root has no ancestor of its own
// (ut-docs#2198).
func setAncestorNames(g *CategoryGroup, ancestor string) {
	for _, c := range g.Children {
		c.AncestorName = ancestor
		setAncestorNames(c, ancestor)
	}
}

// stampLocked (ut-docs#2361) sets ButtonVM.Locked on every button in the
// tree BuildCategoryGroups returned, categorized and uncategorized alike —
// a separate pass rather than a BuildCategoryGroups parameter, so the
// existing call sites (and their tests) building a tree with no notion of
// "granted" at all keep compiling unchanged. Takes granted (matching the
// canPerform-shaped bool the caller actually has) and inverts it once here,
// so every other caller stamps the same tile_sheet.html-style Locked.
func stampLocked(groups []*CategoryGroup, granted bool) {
	locked := !granted
	for _, g := range groups {
		for i := range g.Buttons {
			g.Buttons[i].Locked = locked
		}
		stampLocked(g.Children, granted)
	}
}

// pruneEmptyCategoryGroup drops child branches with no buttons anywhere in
// their subtree and reports whether g itself still has any left.
func pruneEmptyCategoryGroup(g *CategoryGroup) bool {
	kept := g.Children[:0]
	hasAny := len(g.Buttons) > 0
	for _, c := range g.Children {
		if pruneEmptyCategoryGroup(c) {
			kept = append(kept, c)
			hasAny = true
		}
	}
	g.Children = kept
	return hasAny
}

// ButtonStore persists shortcut buttons in the shortcut_buttons table via repo.
type ButtonStore struct {
	repo         *data.ShortcutsRepo
	posRepo      *data.POSRepo
	modRepo      *data.ModifierRepo
	catalogRepo  *data.CatalogRepo
	settingsRepo *data.SettingsRepo
}

func NewButtonStore(db *sql.DB) *ButtonStore {
	return &ButtonStore{
		repo:         data.NewShortcutsRepo(db),
		posRepo:      data.NewPOSRepo(db),
		modRepo:      data.NewModifierRepo(db),
		catalogRepo:  data.NewCatalogRepo(db),
		settingsRepo: data.NewSettingsRepo(db),
	}
}

// CategoriesTabEnabled reports whether ut-docs#2283's optional "Categories"
// tab should render on the sell screen — settings-gated
// (data.SellScreenCategoriesTabKey), default off. A read error is treated
// as "off" by the caller (ButtonsHTTP.List), the same non-fatal-but-logged
// shape LoadCategories already uses for its own error: losing this ONE
// optional tab is much better than failing the whole sale-screen render.
func (s *ButtonStore) CategoriesTabEnabled(ctx context.Context) (bool, error) {
	v, _, err := s.settingsRepo.Get(ctx, data.SellScreenCategoriesTabKey)
	if err != nil {
		return false, err
	}
	return v == "1", nil
}

// LoadCategories returns the flat category list the sale-screen grid nests
// buttons under (see BuildCategoryGroups). ListActiveCategories, not
// ListCategories (ut-docs#1898): a deactivated category must not offer
// itself as a sale-screen tab.
func (s *ButtonStore) LoadCategories(ctx context.Context) ([]data.CategoryNode, error) {
	return s.catalogRepo.ListActiveCategories(ctx)
}

type SearchResult struct {
	ItemID  string
	Name    string
	Barcode string
	SKU     string
	Image   string
}

// AddVals returns the JSON payload the Designer's search-result button
// posts to /api/buttons/add (htmx parses the hx-vals attribute with
// JSON.parse). Marshaled server-side so a name/barcode/path containing a
// double quote or backslash survives the HTML-attribute round trip —
// interpolating the raw fields into a JSON literal inside the template
// produced invalid JSON for any quoted name, silently breaking add.
//
// "code" prefers Barcode, falling back to SKU when Barcode is empty
// (ut-docs#1220): a SKU-only item — loose produce, services, anything with
// no barcode row — otherwise posts code="", which used to make
// ButtonStore.Add reject the add as a 400. As of ut-docs#1459, Add itself
// synthesizes a stable code from itemId when code is still empty here (an
// item with neither a barcode nor a SKU), so this function is left posting
// "" in that remaining case rather than duplicating that fallback — Add is
// the single choke point every caller (this template, the raw API) goes
// through, so it's the one place that needs to know how to cope with no
// code at all. The button-code resolution chain (PriceResolverAdapter)
// already accepts a barcode, a SKU, or Add's synthesized code as "code", so
// neither fallback changes how a code resolves — only which identifier
// gets sent/stored for a given item.
func (r SearchResult) AddVals() string {
	code := r.Barcode
	if code == "" {
		code = r.SKU
	}
	b, _ := json.Marshal(map[string]string{
		"label":    r.Name,
		"code":     code,
		"itemId":   r.ItemID,
		"imageUrl": r.Image,
	})
	return string(b)
}

// SearchItems finds items (and primary barcodes/SKUs) to add as shortcuts.
func (s *ButtonStore) SearchItems(ctx context.Context, q string, offset, limit int) ([]SearchResult, error) {
	repoResults, err := s.posRepo.SearchItemsForShortcuts(ctx, q, offset, limit)
	if err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(repoResults))
	for _, r := range repoResults {
		out = append(out, SearchResult{
			ItemID:  r.ItemID,
			Name:    r.Name,
			Barcode: r.Barcode,
			SKU:     r.SKU,
			Image:   r.Image,
		})
	}
	return out, nil
}

// LoadAllActive returns EVERY active catalog item as a Button-shaped tile
// (ut-docs#2294) — unlike Load (shortcut_buttons rows only, i.e. Designer
// quick buttons), this is sourced straight from the catalog itself
// (CatalogRepo.ListItems, already `WHERE is_active = 1 ORDER BY name`), so
// an item with no quick button still shows on the sell screen's All tab.
// Sorted by name (ListItems' own ORDER BY) — a flat A-Z grid, not grouped
// by category: the card explicitly allows this simplification over
// reusing BuildCategoryGroups' tree machinery for a second, parallel grid;
// noted as a deliberate simplification, not an oversight.
//
// A code with neither a real barcode nor a SKU falls back to the same
// synthesizedButtonCodePrefix scheme ButtonStore.Add already uses for a
// codeless quick button (ut-docs#1459) — resolvable here too because
// ut-docs#2294 also taught POSRepo.ResolveShortcutLineDecoded to resolve
// that prefix straight against the items table, not just against a
// shortcut_buttons row (see internal/data's itemIDCodePrefix).
func (s *ButtonStore) LoadAllActive(ctx context.Context) ([]Button, error) {
	items, err := s.catalogRepo.ListItems(ctx)
	if err != nil {
		return nil, err
	}
	itemIDs := make([]string, 0, len(items))
	for _, it := range items {
		itemIDs = append(itemIDs, it.ID)
	}
	barcodes, err := s.catalogRepo.ItemBarcodes(ctx)
	if err != nil {
		// Not fatal to the render (every tile still shows) — same
		// non-fatal-but-loud treatment Load gives its own batched-lookup
		// errors below. Every tile falls back to its SKU (or the
		// synthesized item: code) instead of a real barcode.
		logging.L().Warnf("ui: load all-active items barcodes failed, tiles fall back to SKU/synthesized code: %v", err)
	}
	thumbs, err := s.catalogRepo.ItemThumbnails(ctx)
	if err != nil {
		logging.L().Warnf("ui: load all-active items thumbnails failed, tiles fall back to no image: %v", err)
	}
	// The next three lookups are chunked (ut-docs#2318): SQLite's bind-
	// variable ceiling (32766) is well within plausible active-catalog
	// sizes when called unchunked with the WHOLE id set —
	// ItemIDsWithModifiers alone binds 2 args per id, so it starts failing
	// past ~16,383 active items. Each of the three functions treated its
	// own failure as non-fatal already (a warn + per-tile fallback), which
	// is safe for Load's small quick-button input but was silently
	// dangerous here: a modifier prompt, a variant prompt or a promotional
	// price could vanish with no visible sign anything went wrong. Chunking
	// keeps every call comfortably under the ceiling regardless of catalog
	// size, and merging per-chunk results means a failure now degrades only
	// the chunk that failed, not the whole active catalog.
	idChunks := data.ChunkStrings(itemIDs, data.IDChunkSize)
	var hasMods map[string]bool
	if s.modRepo != nil {
		hasMods = map[string]bool{}
		for _, chunk := range idChunks {
			m, err := s.modRepo.ItemIDsWithModifiers(ctx, chunk)
			if err != nil {
				// Previously silently discarded (`_`) — now logged like the
				// other two lookups below, since silence is exactly the
				// failure mode this fix exists to remove.
				logging.L().Warnf("ui: load all-active items-with-modifiers failed for a batch of %d item(s), those tiles fall back to plain add-to-basket: %v", len(chunk), err)
				continue
			}
			data.MergeMapInto(hasMods, m)
		}
	}
	var hasVariants map[string]bool
	var currentPrices map[string]int64
	if s.catalogRepo != nil {
		hasVariants = map[string]bool{}
		for _, chunk := range idChunks {
			m, err := s.catalogRepo.ItemIDsWithVariants(ctx, chunk)
			if err != nil {
				// Same money-correctness-affecting non-fatal-but-loud treatment
				// as Load's own hasVariants error handling (ut-docs#2209 review
				// finding 5): on this error every tile in the failed batch falls
				// back to straight-to-basket at the parent's base price.
				logging.L().Warnf("ui: load all-active items-with-variants failed for a batch of %d item(s), those tiles fall back to parent-price add (ut-docs#2209): %v", len(chunk), err)
				continue
			}
			data.MergeMapInto(hasVariants, m)
		}
		currentPrices = map[string]int64{}
		for _, chunk := range idChunks {
			p, err := s.catalogRepo.ItemCurrentPrices(ctx, chunk)
			if err != nil {
				// Same as Load's own currentPrices error handling (ut-docs#2258).
				logging.L().Warnf("ui: load all-active current prices failed for a batch of %d item(s), those tiles fall back to raw base_price (ut-docs#2258): %v", len(chunk), err)
				continue
			}
			data.MergeMapInto(currentPrices, p)
		}
	}
	out := make([]Button, 0, len(items))
	for _, it := range items {
		code := ""
		if bcs := barcodes[it.ID]; len(bcs) > 0 {
			code = bcs[0] // ItemBarcodes orders primary first
		}
		if code == "" {
			code = it.SKU
		}
		if code == "" {
			code = synthesizedButtonCodePrefix + it.ID
		}
		price := it.BasePrice
		if p, ok := currentPrices[it.ID]; ok {
			price = p
		}
		catID := ""
		if it.CategoryID != nil {
			catID = *it.CategoryID
		}
		out = append(out, Button{
			Label:        it.Name,
			Code:         code,
			ItemID:       it.ID,
			ImageURL:     thumbs[it.ID],
			Price:        price,
			HasModifiers: hasMods[it.ID],
			HasVariants:  hasVariants[it.ID],
			CategoryID:   catID,
			Color:        it.Color,
		})
	}
	return out, nil
}

// SearchSellable finds every active catalog item matching q — the
// sell-screen's own live search (ut-docs#2294), distinct from SearchItems
// (which returns a lighter SearchResult shape for the Designer's
// add-to-shortcuts flow). Returns tile-ready Buttons so results render
// through the exact same "product-tile" template a quick-button/All-tab
// tile already uses (identical price/variant/modifier/color behavior),
// rather than the Designer's own "tap to add as a shortcut" affordance.
func (s *ButtonStore) SearchSellable(ctx context.Context, q string, limit int) ([]Button, error) {
	if limit <= 0 {
		limit = 20
	}
	results, err := s.posRepo.SearchItemsForShortcuts(ctx, q, 0, limit)
	if err != nil {
		return nil, err
	}
	itemIDs := make([]string, 0, len(results))
	for _, r := range results {
		itemIDs = append(itemIDs, r.ItemID)
	}
	var hasMods map[string]bool
	if s.modRepo != nil {
		hasMods, err = s.modRepo.ItemIDsWithModifiers(ctx, itemIDs)
		if err != nil {
			// Previously silently discarded (`_`) — same non-fatal-but-loud
			// treatment as the hasVariants/currentPrices lookups just below
			// (ut-docs#2454; mirrors ut-docs#2318/#2451's identical fix
			// elsewhere in this file).
			logging.L().Warnf("ui: search-sellable items-with-modifiers failed, every result falls back to plain add-to-basket: %v", err)
		}
	}
	var hasVariants map[string]bool
	var currentPrices map[string]int64
	if s.catalogRepo != nil {
		hasVariants, err = s.catalogRepo.ItemIDsWithVariants(ctx, itemIDs)
		if err != nil {
			logging.L().Warnf("ui: search-sellable items-with-variants failed, every result falls back to parent-price add (ut-docs#2209): %v", err)
		}
		currentPrices, err = s.catalogRepo.ItemCurrentPrices(ctx, itemIDs)
		if err != nil {
			logging.L().Warnf("ui: search-sellable current prices failed, every result falls back to raw base_price (ut-docs#2258): %v", err)
		}
	}
	out := make([]Button, 0, len(results))
	for _, r := range results {
		code := r.Barcode
		if code == "" {
			code = r.SKU
		}
		if code == "" {
			code = synthesizedButtonCodePrefix + r.ItemID
		}
		price := r.BasePrice
		if p, ok := currentPrices[r.ItemID]; ok {
			price = p
		}
		out = append(out, Button{
			Label:        r.Name,
			Code:         code,
			ItemID:       r.ItemID,
			ImageURL:     r.Image,
			Price:        price,
			HasModifiers: hasMods[r.ItemID],
			HasVariants:  hasVariants[r.ItemID],
			Color:        r.Color,
		})
	}
	return out, nil
}

func (s *ButtonStore) Load() ([]Button, error) {
	ctx := context.Background()
	rows, err := s.repo.LoadButtons(ctx)
	if err != nil {
		return nil, err
	}
	itemIDs := make([]string, 0, len(rows))
	for _, b := range rows {
		if b.ItemID != "" {
			itemIDs = append(itemIDs, b.ItemID)
		}
	}
	var hasMods map[string]bool
	if s.modRepo != nil {
		var err error
		hasMods, err = s.modRepo.ItemIDsWithModifiers(ctx, itemIDs)
		if err != nil {
			// Previously silently discarded (`_`) — same non-fatal-but-loud
			// treatment as the hasVariants/currentPrices lookups just below
			// (ut-docs#2454; mirrors ut-docs#2318/#2451's identical fix
			// elsewhere in this file).
			logging.L().Warnf("ui: load items-with-modifiers failed, every tile falls back to plain add-to-basket: %v", err)
		}
	}
	var hasVariants map[string]bool
	var currentPrices map[string]int64
	if s.catalogRepo != nil {
		var err error
		hasVariants, err = s.catalogRepo.ItemIDsWithVariants(ctx, itemIDs)
		if err != nil {
			// Not fatal — a tile still renders — but do NOT let it pass in
			// silence (ut-docs#2209 review, finding 5). On this error EVERY
			// tile falls back to straight-to-basket at the parent's base
			// price, i.e. ut-docs#2209 returns shop-wide, and the only
			// symptom is wrong takings. The neighbouring hasMods swallow
			// loses a prompt; this one loses money correctness, so it gets
			// the same treatment ButtonsHTTP.List gives its own non-fatal
			// category error.
			logging.L().Warnf("ui: load items-with-variants failed, every tile falls back to parent-price add (ut-docs#2209): %v", err)
		}
		currentPrices, err = s.catalogRepo.ItemCurrentPrices(ctx, itemIDs)
		if err != nil {
			// Same non-fatal-but-loud treatment as hasVariants above
			// (ut-docs#2258): on this error every tile falls back to the
			// STALE configured base_price it already carries in b.Price,
			// same failure shape as ut-docs#2209's own hasVariants gap.
			logging.L().Warnf("ui: load item current prices failed, every tile falls back to raw base_price (ut-docs#2258): %v", err)
		}
	}
	var out []Button
	for _, b := range rows {
		// ut-docs#2258: prefer the batched price_history-aware price;
		// b.Price (raw base_price from LoadButtons) is the fallback for an
		// id ItemCurrentPrices didn't return (lookup error, or the item
		// row is gone) — never a silent zero.
		price := b.Price
		if p, ok := currentPrices[b.ItemID]; ok {
			price = p
		}
		out = append(out, Button{
			Label:        b.Label,
			Code:         b.Barcode,
			ItemID:       b.ItemID,
			ImageURL:     b.ImageURL,
			Price:        price,
			HasModifiers: hasMods[b.ItemID],
			HasVariants:  hasVariants[b.ItemID],
			CategoryID:   b.CategoryID,
			Color:        b.Color,
		})
	}
	return out, nil
}

func (s *ButtonStore) Save(list []Button) error {
	var repoButtons []data.ShortcutButton
	for _, b := range list {
		repoButtons = append(repoButtons, data.ShortcutButton{
			Label:    b.Label,
			Barcode:  b.Code,
			ItemID:   b.ItemID,
			ImageURL: b.ImageURL,
		})
	}
	return s.repo.SaveButtons(context.Background(), repoButtons)
}

// UpdateOrder persists a new tile order (codes in display order).
func (s *ButtonStore) UpdateOrder(ctx context.Context, codes []string) error {
	return s.repo.UpdateOrder(ctx, codes)
}

// synthesizedButtonCodePrefix marks a shortcut-button code that ButtonStore.Add
// generated itself (ut-docs#1459) rather than one carrying a real barcode or
// SKU — see Add below. Never a real barcode/SKU value, so it's a safe
// signal for anywhere downstream that must not present it as if it were
// one (PriceResolverAdapter.resolve blanks it off the basket line's SKU
// rather than let a raw item UUID reach a receipt or the journal).
const synthesizedButtonCodePrefix = "item:"

func (s *ButtonStore) Add(btn Button) error {
	btn.Label = strings.TrimSpace(btn.Label)
	btn.Code = strings.TrimSpace(btn.Code)
	btn.ItemID = strings.TrimSpace(btn.ItemID)
	if btn.Label == "" || btn.ItemID == "" {
		return errors.New("label and itemId are required")
	}
	if btn.Code == "" {
		// ut-docs#1459: an item with neither a barcode nor a SKU (loose
		// produce with no identifier at all, or any CSV row imported with
		// both columns blank) reaches here with code="" even after
		// AddVals's barcode->SKU fallback (ut-docs#1220) — there is
		// nothing left to fall back to. shortcut_buttons.barcode is this
		// table's PRIMARY KEY, so "no code" isn't a state the row can be
		// in at all; itemId already uniquely identifies the item (items.id
		// is itself a primary key), so a stable synthetic code derived
		// from it is a safe substitute for real cataloguing data. Prefixed
		// so it can never collide with a real scanned barcode or a
		// human-entered SKU. Deterministic per item, so re-adding the same
		// codeless item (or the ON CONFLICT(barcode) upsert below) targets
		// the same row rather than creating a duplicate.
		btn.Code = synthesizedButtonCodePrefix + btn.ItemID
	}
	return s.repo.AddButton(context.Background(), data.ShortcutButton{
		Label:    btn.Label,
		Barcode:  btn.Code,
		ItemID:   btn.ItemID,
		ImageURL: btn.ImageURL,
	})
}

func (s *ButtonStore) Remove(code string) error {
	return s.repo.RemoveButton(context.Background(), code)
}

/* ----------------- HTTP handlers (htmx-friendly) ----------------- */

type TplRenderer interface {
	Render(w http.ResponseWriter, name string, data any) error
}

type Renderer struct {
	t *template.Template
}

// stripWebPrefix converts a caller-supplied disk-style path
// (filepath.Join("web", "ui", ...)) into the path used inside the embedded
// web.FS ("ui/...", no "web/" prefix — the FS root already is web/).
func stripWebPrefix(path string) string {
	return strings.TrimPrefix(filepath.ToSlash(path), "web/")
}

// NewRenderer's (layout, page, partial) file set only takes a handful of
// distinct values across all call sites (buttons_api.go), so it's cached
// per that tuple and cloned per call thereafter (ut-docs#1320) — see
// httpx.ClonedTemplate.
func NewRenderer(layout, page, partial string, funcs template.FuncMap) (*Renderer, error) {
	key := "ui.Renderer:" + layout + "|" + page + "|" + partial
	t, err := httpx.ClonedTemplate(key, "base.html", funcs,
		stripWebPrefix(layout),
		stripWebPrefix(page),
		"ui/partials/nav.html",
		// base.html references it on every page; must be parsed alongside
		// the layout or executing "base" fails.
		"ui/partials/bugreport_panel.html",
		// ut-docs#2183: base.html itself now includes the shared
		// #pos-alert partial on every page (moved there from
		// index.html/admin.html/items.html's own content blocks). Parsing/
		// Clone succeed either way — html/template only resolves a
		// {{ template "name" }} call at EXECUTE time — but production
		// callers in internal/pages/buttons_api.go only ever execute the
		// "buttons"/"buttons_admin_grid" fragments (never "base"), while
		// render_cwd_test.go's TestButtonsNewRenderer_WorksFromAnyWorkingDirectory
		// does execute "base" through this exact renderer, so
		// {{ define "pos_alert" }} still has to be present in this parsed
		// set or THAT executes and fails with "no such template". Same
		// reasoning as bugreport_panel.html just above (corrected here,
		// ut-docs#2179's original comment on this line also said "parse
		// time", which independent review caught as inaccurate).
		"ui/partials/pos_alert.html",
		stripWebPrefix(partial),
	)
	if err != nil {
		return nil, err
	}
	return &Renderer{t: t}, nil
}

func (r *Renderer) Render(w http.ResponseWriter, name string, data any) error {
	return r.t.ExecuteTemplate(w, name, data)
}

type ButtonsHTTP struct {
	Store ButtonStore
	View  TplRenderer
	// HideAllTab (ut-docs#2294) turns off the sell screen's All tab —
	// settings.sale.show_all_tab, default ON. Named in the INVERTED sense
	// (like catalogtypes.ItemInput.StockUntracked) so the Go zero value
	// (false) means "show it": every existing &ButtonsHTTP{Store: ...,
	// View: ...} literal in this package's own test suite (and in
	// internal/pages/buttons_api.go before this card) leaves this field
	// unset, and the actual settings default is ALSO "on" — a
	// straight-named ShowAllTab field would have silently flipped every
	// one of those to "off" instead.
	HideAllTab bool
	// Granted (ut-docs#2361) is this request's catalog_management
	// permission check result, resolved by the caller (registerButtonsAPI,
	// which has the *common.Deps and *http.Request canPerform needs —
	// internal/ui cannot call internal/pages.canPerform itself without an
	// import cycle). Zero value is false, i.e. "not granted": every
	// existing test that constructs a ButtonsHTTP without setting this
	// field renders as if for a non-granted cashier, which is the safe
	// default and matches none of those tests' assertions touching the
	// jiggle-mode lock affordance either way.
	Granted bool
	// Designer (ut-docs#2174) renders the SAME buttons.html fragment as the
	// Designer page's live replica of the sale screen (GET
	// /ui/designer/buttons, designer_page.go) instead of the sale screen
	// itself: empty categories are kept (BuildCategoryGroupsKeepEmpty —
	// an editor must show the category you just created), the root
	// refetches itself from the Designer route on buttons-changed, the
	// strip's pencil becomes the edit-mode toggle instead of a link to
	// /designer, and every category tab gains its edit affordances
	// (pencil + popover, + tab). The tile grid itself is byte-for-byte the
	// sale screen's — that is the point: one template, no fork.
	Designer bool
}

// AllTabPageSize bounds how many of the sell screen's All-tab items
// (ButtonStore.LoadAllActive) any single response ships (ut-docs#2319):
// GET /ui/buttons used to inline EVERY active item into the All grid every
// time it rendered — including on the "modifiers-changed"/"buttons-changed"
// whole-document refetch buttons.html's root wires up on every modifier/
// button-config change — so a 2000-item catalog shipped ~1MB of HTML on
// each such edit. 200 matches ut-docs#2294's own "usable with a 200+ item
// catalog" acceptance bar: a catalog at or under that size still renders in
// one response exactly as before (no load-more button appears at all), so
// this only starts bounding cost once a catalog crosses the size #2294
// already committed to supporting without complaint.
const AllTabPageSize = 200

// pageButtons slices all starting at offset, returning at most
// AllTabPageSize items and whether more remain beyond this page. offset
// past the end of all returns an empty page with hasMore false, never a
// panic — a stale/hand-edited offset query param is untrusted input.
func pageButtons(all []Button, offset int) (page []Button, hasMore bool) {
	if offset < 0 || offset >= len(all) {
		return nil, false
	}
	end := offset + AllTabPageSize
	if end >= len(all) {
		return all[offset:], false
	}
	return all[offset:end], true
}

func (h *ButtonsHTTP) List(w http.ResponseWriter, r *http.Request) {
	btns, _ := h.Store.Load()
	cats, err := h.Store.LoadCategories(r.Context())
	if err != nil {
		// Not fatal to the render — every button still shows, just
		// ungrouped (BuildCategoryGroups buckets them as uncategorized)
		// — but worth a log line: a till stuck like this permanently
		// loses category grouping/coloring with no visible sign why.
		logging.L().Errorf("buttons list: load categories: %v", err)
	}
	// ut-docs#2294: the All grid is loaded once per /ui/buttons render
	// (this handler is only ever fetched on page load and on
	// "modifiers-changed from:body" -- see buttons.html's own top comment
	// -- never on a basket mutation), not re-queried per basket change.
	// Skipped entirely when the setting is off, so a till that never wants
	// the tab pays nothing for it.
	var allPage []Button
	var allHasMore bool
	if !h.HideAllTab {
		allBtns, err := h.Store.LoadAllActive(r.Context())
		if err != nil {
			logging.L().Errorf("buttons list: load all-active items: %v", err)
		}
		// ut-docs#2319: only the first page ships on the initial render (and
		// on every modifiers-changed/buttons-changed whole-document
		// refetch) — see AllTabPageSize's own doc comment. The rest loads
		// on demand via the "load more" button AllMore serves below.
		allPage, allHasMore = pageButtons(allBtns, 0)
	}
	categoriesTabEnabled, err := h.Store.CategoriesTabEnabled(r.Context())
	if err != nil {
		// Same non-fatal-but-logged shape as the categories load above
		// (ut-docs#2283) — a settings-read error just means the optional
		// tab stays off this render, not that the whole sale screen fails.
		logging.L().Warnf("buttons list: load categories-tab setting: %v", err)
	}
	var groups []*CategoryGroup
	if h.Designer {
		groups = BuildCategoryGroupsKeepEmpty(btns, cats)
	} else {
		groups = BuildCategoryGroups(btns, cats)
	}
	stampLocked(groups, h.Granted)
	_ = h.View.Render(w, "buttons", map[string]any{
		"Groups":               groups,
		"AllButtons":           ToVM(allPage),
		"AllHasMore":           allHasMore,
		"AllNextOffset":        len(allPage),
		"ShowAllTab":           !h.HideAllTab,
		"CategoriesTabEnabled": categoriesTabEnabled,
		"Designer":             h.Designer,
		// The category popovers' colour swatches: the SAME fixed palette
		// /categories' dialog and the item editor offer (validated
		// server-side by catalogtypes.ValidItemColor in
		// designer_categories_api.go). Only read in Designer mode.
		"ItemColors": catalogtypes.ItemColors(),
	})
}

// AllMore renders the next page of the sell screen's All tab (ut-docs#2319)
// — the same offset-paginated "load more" shape
// internal/pages/buttons_api.go's /api/buttons/search already uses for the
// Designer's own item search, applied here to bound GET /ui/buttons's
// response size instead. Reloads the full active set via LoadAllActive on
// every call rather than caching a page cursor server-side: this is a
// deliberate simplification (the card's own "lean on search/page it"
// options are a fix for response SIZE, not for LoadAllActive's own query
// cost, which #2318's chunking already bounds against SQLite's bind
// ceiling regardless of catalog size) — a follow-up card can revisit
// per-request caching if the extra query load ever proves to matter in
// practice.
func (h *ButtonsHTTP) AllMore(w http.ResponseWriter, r *http.Request) {
	offset := 0
	if off := r.URL.Query().Get("offset"); off != "" {
		if v, err := strconv.Atoi(off); err == nil && v >= 0 {
			offset = v
		}
	}
	all, err := h.Store.LoadAllActive(r.Context())
	if err != nil {
		logging.L().Errorf("buttons all-more: load all-active items: %v", err)
	}
	page, hasMore := pageButtons(all, offset)
	_ = h.View.Render(w, "all-more-fragment", map[string]any{
		"Buttons":    ToVM(page),
		"HasMore":    hasMore,
		"NextOffset": offset + len(page),
	})
}

// Search renders the sell screen's live, server-backed search results
// (ut-docs#2294): every active catalog item matching q, as the same
// "product-tile" component a quick-button/All-tab tile already uses — not
// just whatever happens to be rendered in the currently active tab. An
// empty q renders nothing (the client only wires this up while the search
// box actually has input in it — see buttons.html's own search comments).
func (h *ButtonsHTTP) Search(w http.ResponseWriter, r *http.Request) {
	q := strings.TrimSpace(r.URL.Query().Get("q"))
	var results []Button
	if q != "" {
		var err error
		results, err = h.Store.SearchSellable(r.Context(), q, 30)
		if err != nil {
			logging.L().Errorf("buttons search: %v", err)
		}
	}
	_ = h.View.Render(w, "products-search-results", map[string]any{
		"Results": ToVM(results),
		"Query":   q,
	})
}

// Add returns whether the button was actually persisted -- ut-docs#2358:
// the caller (registerButtonsAPI) needs a success/failure signal to call
// auditButtonsElevated symmetrically with move/reorder, which write their
// own audit row directly rather than through a handler-shaped method like
// this one. false on every early-return (bad form, store validation
// failure); true only once Store.Add has actually succeeded.
func (h *ButtonsHTTP) Add(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	itemID := strings.TrimSpace(r.Form.Get("itemId"))
	img := strings.TrimSpace(r.Form.Get("imageUrl"))
	if img != "" && !strings.HasPrefix(img, "http://") && !strings.HasPrefix(img, "https://") && !strings.HasPrefix(img, "/public/") {
		// Treat as filename in local images folder
		img = "/public/images/" + img
	}
	err := h.Store.Add(Button{
		Label:    r.Form.Get("label"),
		Code:     r.Form.Get("code"),
		ItemID:   itemID,
		ImageURL: img,
	})
	if err != nil {
		// ut-docs#1220: a raw http.Error(w, err.Error(), 400) here was
		// invisible to the operator -- buttons_admin.html's search-result
		// button hides the search dropdown on htmx:afterRequest regardless
		// of success (fixed alongside this), and htmx never swaps a
		// non-2xx response into hx-target by default, so the failure had
		// nowhere to go. Render the same "localized HTML fragment +
		// htmx:responseError listener" pattern shifts.html/
		// plugin_settings.html already use, so the page's own script can
		// swap it into a dedicated error element instead.
		logging.L().Infof("[buttons] add: %v", err)
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<div class="error">` + html.EscapeString(httpx.T(locale, designerErrorServerKey)) + `</div>`))
		return false
	}
	// ut-docs#2285: every route that changes the button SET sets the same
	// HX-Trigger, so the one listener (buttons.html's root,
	// hx-trigger="... buttons-changed from:body") refreshes the sale screen
	// whichever route was used. An HX-Trigger only ever dispatches in the
	// document that made the request, so on the Designer page (where /add
	// is actually called from, and nothing listens for buttons-changed)
	// this header is a harmless no-op — it does NOT reach another open
	// tab/window. Set unconditionally anyway so the contract is "the
	// button set changed => buttons-changed", with no per-route exceptions
	// for a future caller to trip over.
	w.Header().Set("HX-Trigger", "buttons-changed")
	// ut-docs#2174: no body. This used to re-render buttons_admin.html's
	// flat "buttons_admin_grid" for the Designer to swap in; that grid is
	// retired — the Designer now hosts a live replica of the sale screen
	// (the same self-refreshing buttons.html root), and the HX-Trigger
	// above is what refreshes it, exactly as /api/buttons/reorder already
	// worked. 204 is what htmx expects for "trigger, don't swap".
	w.WriteHeader(http.StatusNoContent)
	return true
}

// Remove returns whether the button was actually deleted -- ut-docs#2358,
// same rationale as Add's own doc comment above: false on every
// early-return, true only once Store.Remove has actually succeeded.
func (h *ButtonsHTTP) Remove(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	if err := h.Store.Remove(r.Form.Get("code")); err != nil {
		// ut-docs#1697: this raw http.Error(w, err.Error(), 400) used to be
		// harmless -- htmx discards a non-2xx body from hx-target by
		// default, so it went nowhere -- but buttons_admin.html's
		// htmx:responseError listener now also covers the remove form (to
		// surface the requirePrimary refusal added by this same card), so
		// a genuine store error here would paint a raw Go/SQL error string
		// straight onto the operator's screen: exactly what
		// common.LogAndLocalizedError's own doc comment (ut-docs#316)
		// warns a raw error must never do. Same localized-fragment fix as
		// Add above.
		logging.L().Infof("[buttons] remove: %v", err)
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<div class="error">` + html.EscapeString(httpx.T(locale, designerErrorServerKey)) + `</div>`))
		return false
	}
	// ut-docs#2285: see Add's comment above — same contract. This is the
	// one that actually matters on the sale screen: the jiggle edit mode's
	// per-tile remove badge (ut-docs#2339, buttons.html's product-tile)
	// posts to this same route, and this header is what makes the grid
	// drop the tile without a reload.
	w.Header().Set("HX-Trigger", "buttons-changed")
	// ut-docs#2174: no body, same as Add above.
	w.WriteHeader(http.StatusNoContent)
	return true
}

type PriceResolverAdapter struct{ Store *ButtonStore }

// Resolve looks up code against the full variant/item/shortcut/SKU/name
// chain (a.resolve, backed by POSRepo.ResolveShortcutLineDecoded) exactly
// once (ut-docs#1660). Before this, Resolve probed the chain once per
// candidate shape it was checking for (variant, then item, then shortcut,
// then a SKU/name fallback) and discarded every result whose shape didn't
// match — up to 4 identical round trips for the same code, since the chain
// is a pure, deterministic lookup: the SAME code always yields the SAME
// row. There was never a second, differently-scoped search hiding in that
// fallthrough (resolveShortcut's "has an ItemID" check could never fire —
// resolveItem already claims every match with an ItemID and no VariantID,
// and resolveVariant already claims every match with a VariantID; the
// old resolveTextSearch was reached only by a match with neither, and
// returned true unconditionally, whatever the shape) — every shape that
// resolves at all was always going to end up returning true, just after
// wastefully re-querying to find out.
//
// The one genuine second case is a code with surrounding whitespace: every
// current caller (internal/pos.Service) already trims before calling
// Resolve, so this never fires in production, but Resolve is a public
// method satisfying the pos.PriceResolver interface, and a direct/future
// caller could pass untrimmed input — so a second, trimmed attempt is kept
// as an explicit, conditional fallback (only reached when the raw lookup
// missed AND trimming would actually change the query), not a fixed extra
// call on every miss.
func (a PriceResolverAdapter) Resolve(code string) (pos.BasketLine, bool) {
	ctx := context.Background()

	if line, ok := a.resolve(ctx, code); ok {
		return line, true
	}
	trimmed := strings.TrimSpace(code)
	if trimmed == "" || trimmed == code {
		return pos.BasketLine{}, false
	}
	return a.resolve(ctx, trimmed)
}

func (a PriceResolverAdapter) resolve(ctx context.Context, code string) (pos.BasketLine, bool) {
	row, dec, ok := a.Store.posRepo.ResolveShortcutLineDecoded(ctx, code)
	if !ok {
		return pos.BasketLine{}, false
	}
	// ut-docs#1459: for a shortcut-button match, row.SKU is actually the
	// button's own code (data.POSRepo.toShortcutLine), not the item's real
	// SKU — and that code is now sometimes ButtonStore.Add's synthesized
	// "item:<uuid>" rather than a real barcode/SKU. Never let that internal
	// id reach a basket line: it flows straight to sale_lines.sku_snapshot
	// and prints on the receipt / shows in the journal when the shop has
	// "Show SKU" on, exactly the raw-UUID-leak class ut-docs#1176 already
	// fixed once for the catalog's own SKU column.
	sku := row.SKU
	if strings.HasPrefix(sku, synthesizedButtonCodePrefix) {
		sku = ""
	}
	line := pos.BasketLine{
		SKU:        sku,
		Name:       row.Name,
		Qty:        1,
		PriceCents: money.FromMinor(row.Price),
		ItemID:     row.ItemID,
		VariantID:  row.VariantID,
		TaxRateBP:  row.TaxRateBP,
		TaxCodeID:  row.TaxCodeID,
		IsWeighed:  row.IsWeighed,
	}
	if row.ImageURL != "" {
		line.ImageURL = row.ImageURL
	}
	// Embedded-data decode (ADR-0059 §3, ut-docs#934). A weight-embedded
	// scale label carries the quantity in the code itself: Qty is the
	// decoded weight (kilograms — matching the weighed-item convention the
	// qty box already uses) and PriceCents stays the item's per-unit rate,
	// so the existing AmountForQuantity math prices the line with no new
	// mechanism. A ZERO decoded weight is kept as scanned (a visible,
	// voidable zero-amount line), never treated as a parse failure — see
	// barcode.Decoded's doc.
	if dec.HasEmbeddedWeight {
		if w, err := strconv.ParseFloat(dec.EmbeddedWeight, 64); err == nil {
			line.Qty = w
			line.QtyFromCode = true
		} else {
			// internal/barcode always formats EmbeddedWeight as "%d.%03d"
			// (registry.go), so this should be unreachable — but if it ever
			// isn't, fail safe: keep the caller-supplied qty (QtyFromCode
			// stays false) rather than silently losing the decoded weight
			// with no trace.
			logging.L().Warnf("ui: embedded weight %q unparseable for code %q: %v", dec.EmbeddedWeight, code, err)
		}
	}
	// A price-embedded label states an ABSOLUTE price for that one unit:
	// Qty is fixed at 1 and the line must never merge into (or be merged
	// from) another line — mergeResolved's combine step overwrites
	// PriceCents, which would silently drop one label's price. A zero
	// decoded price likewise stays a visible zero-priced line.
	if dec.HasEmbeddedPrice {
		line.PriceCents = dec.EmbeddedPrice
		line.Qty = 1
		line.QtyFromCode = true
		line.NoMerge = true
	}
	return line, true
}
