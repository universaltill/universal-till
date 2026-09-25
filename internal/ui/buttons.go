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
	"time"

	"github.com/universaltill/universal-till/internal/barcode"
	"github.com/universaltill/universal-till/internal/catalogtypes"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
	"github.com/universaltill/universal-till/internal/iconid"
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
	// QuickButton marks an explicit shortcut_buttons row (set by LoadWith),
	// as opposed to an implicit tile derived from the active catalog. Only
	// an explicit quick button survives its category being hidden from the
	// sale screen (manage-shop catalog contract §3.2) — see
	// BuildCategoryGroups. Never on the wire.
	QuickButton bool `json:"-"`
	// Hidden (ut-docs#2698) marks an item hidden from the sell screen
	// (items.sell_screen_hidden). Only the GRID loads (Load/LoadWith) carry
	// hidden tiles at all -- in their own spot, so edit mode can show them
	// greyed with an Unhide badge while CSS keeps them out of sight at rest.
	// Every at-rest browse surface (LoadAllActive, the All grid, category
	// tiles and popups) leaves them out. SearchSellable sets it too, to offer
	// "Add to quick buttons" on the result. Never on the wire.
	Hidden bool `json:"-"`
	// Removed (ut-docs#2698) marks an item taken off the quick buttons by
	// the trash badge (items.sell_screen_removed). Only SearchSellable ever
	// returns such an item -- every grid/browse load leaves it out. Never on
	// the wire.
	Removed bool `json:"-"`
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
	// Editing (ut-docs#2174) is true when this tile renders inside the
	// Designer's live replica of the sale screen (GET /ui/buttons?mode=edit)
	// rather than on the sale screen itself. product-tile (buttons.html)
	// then renders the tile INERT — no /api/pos/scan or modifier-picker
	// wiring, so a tap on the Designer can never add to the cashier's live
	// basket — while keeping data-code/data-pos and the .tile-cell/badge
	// markup app.js's utTileJiggle keys off, so long-press/drag/arrow-key
	// reorder works there unchanged; the edit badge's return URL points back
	// at /designer instead of /. Per-request like Locked, stamped on by List
	// via stampEditing for the same reason (no per-button source).
	Editing bool `json:"-"`
	// Hidden (ut-docs#2698): see Button.Hidden. product-tile (buttons.html)
	// renders the tile greyed with an eye-off glyph and an Unhide badge in
	// place of Hide; on the sale screen (not .Editing) it also gets the
	// class that keeps it out of sight unless the grid is in jiggle mode.
	Hidden bool `json:"-"`
	// NotOnGrid (ut-docs#2698) is true for a sell-screen search result whose
	// item is not a visible quick button (hidden or removed) -- the result
	// then carries an "Add to quick buttons" action, shown in edit mode only.
	NotOnGrid bool `json:"-"`
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
		Hidden:       x.Hidden,
		NotOnGrid:    x.Hidden || x.Removed,
	}
}

// visibleOnly (ut-docs#2698) drops hidden tiles, for every at-rest browse
// surface that has no edit mode of its own (the All grid, category tiles and
// popups). It returns a new slice; the input is left untouched.
func visibleOnly(buttons []Button) []Button {
	out := make([]Button, 0, len(buttons))
	for _, b := range buttons {
		if !b.Hidden {
			out = append(out, b)
		}
	}
	return out
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

	// ImageURL (ut-docs#2500) is the category's image for its strip tab
	// and overflow tile, already through categoryImageURL — "" means
	// render no <img> at all.
	ImageURL string

	// AncestorName is the top-level root's Name for a NESTED subcategory —
	// empty for a root itself (a root has no ancestor to disambiguate
	// against). ut-docs#2198: two subcategories sharing a name under
	// different top-level categories (e.g. Food>Specials and
	// Household>Specials) are indistinguishable once both are visible at
	// once (a cross-category search), so the
	// template prefixes a nested header with this field while that
	// ambiguity is possible. Set once, at build time, to the root's name
	// only (not the immediate parent's) even for a grandchild — "at least
	// the top-level category name" is what the card asks for, and a
	// shallow label avoids a second breadcrumb-truncation problem this
	// card never scoped.
	AncestorName string

	// HasButtons (ut-docs#2498) is true when this group OR ANY descendant
	// has at least one manually-configured quick button — the OLD survival
	// test pruneEmptyCategoryGroup used before this card, kept as its own
	// field because a group can now survive pruning a second way (active
	// catalog items with zero quick buttons anywhere in its subtree). The
	// template uses this, not "survived pruning," to decide whether to
	// render the button grid or an empty-state message: a group that is
	// present ONLY because of item counts has nothing to grid.
	//
	// ut-docs#2698: only VISIBLE (not hidden) quick buttons count here -- a
	// group whose tiles are all hidden reads as "no quick buttons" at rest.
	HasButtons bool

	// HasVisible (ut-docs#2698) is true when this group or any descendant has
	// something to show AT REST: a visible quick button or an active,
	// not-hidden catalog item. A group can now survive pruning on hidden
	// tiles alone (edit mode shows them greyed); with HasVisible false its
	// tab/overflow tile is marked so CSS keeps it out of sight at rest.
	HasVisible bool
}

// VisibleButtons (ut-docs#2698) is how many of the group's OWN buttons are
// not hidden -- the at-rest count the overflow sheet shows.
func (g *CategoryGroup) VisibleButtons() int {
	n := 0
	for _, b := range g.Buttons {
		if !b.Hidden {
			n++
		}
	}
	return n
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

// categoryImageURL (ut-docs#2500) turns a stored categories.image_path into
// what the sell screen may render: the path is kept only when it resolves
// to a file this till can actually serve (httpx.AssetExists — the data
// dir, the release tree, or the binary's embedded web/ assets). A built-in
// icon (/public/assets/category-icons/..., shipped with every binary)
// therefore always renders; an uploaded photo
// (/public/assets/categories/<id>/thumb.png) renders only where the file
// is — the column rides the admin sync bundle but the file does not (the
// D2 limit), so on a satellite the category shows name-only instead of a
// broken <img>. The same check covers a built-in key a newer primary knows
// and this older satellite doesn't ship yet. Anything not under /public/
// is dropped outright: the column arrives over sync, so it is untrusted.
func categoryImageURL(path string) string {
	if path == "" || !strings.HasPrefix(path, "/public/") || strings.Contains(path, "..") {
		return ""
	}
	if !httpx.AssetExists(path) {
		return ""
	}
	return path
}

// BuildCategoryGroups nests buttons under their item's category (following
// each category's ParentID to build the tree cats itself doesn't carry
// nesting for) — "deep category trees, not a flat product list." Branches
// with no buttons AND no active catalog items anywhere in their subtree are
// pruned so an unused imported category never shows as an empty header on
// the till (ut-docs#2498: a category with active items but zero quick
// buttons must still surface — see pruneEmptyCategoryGroup and
// CategoryGroup.HasButtons for how the two survival paths are told apart).
// itemCounts maps a category ID to its own DIRECT active-item count (e.g.
// data.CategoryAdminRow.ItemCount); nil/missing entries are treated as
// zero, which is a safe default for callers that don't care about this
// axis (existing tests, mainly). Buttons with no category, or a
// category_id that no longer resolves, land in a trailing synthetic
// bucket (ID == ""), included only when non-empty.
func BuildCategoryGroups(buttons []Button, cats []data.CategoryNode, itemCounts map[string]int) []*CategoryGroup {
	byID := make(map[string]*CategoryGroup, len(cats))
	nodeByID := make(map[string]data.CategoryNode, len(cats))
	for _, c := range cats {
		byID[c.ID] = &CategoryGroup{ID: c.ID, Name: c.Name, Color: resolveCategoryColor(c), ImageURL: categoryPicture(c)}
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

	// Manage-shop catalog contract §3.2: a category whose
	// show_on_sale_screen is off leaves the strip/tabs/overflow with its
	// whole subtree, but its items stay sellable by search, scan AND quick
	// buttons. So an explicit quick button (Button.QuickButton) anywhere in
	// a hidden subtree moves to the uncategorised bucket — never off the
	// sale screen (the strip has no All tab since ut-docs#2613) — while an
	// implicit catalog tile leaves with its category (the item still sells
	// by search and scan, and shows in the all_filter_chips All grid).
	var uncategorized []ButtonVM
	for i, b := range buttons {
		vm := toButtonVM(b)
		vm.Pos = i // global sort index — see ButtonVM.Pos
		g, ok := byID[b.CategoryID]
		if b.CategoryID == "" || !ok || (b.QuickButton && inHiddenCategorySubtree(b.CategoryID, nodeByID)) {
			uncategorized = append(uncategorized, vm)
			continue
		}
		g.Buttons = append(g.Buttons, vm)
	}

	roots = dropHiddenGroups(roots, nodeByID)

	kept := roots[:0]
	for _, g := range roots {
		if pruneEmptyCategoryGroup(g, itemCounts) {
			kept = append(kept, g)
		}
	}
	roots = kept

	for _, g := range roots {
		setAncestorNames(g, g.Name)
	}

	if len(uncategorized) > 0 {
		u := &CategoryGroup{Color: uncategorizedColor, Buttons: uncategorized}
		// ut-docs#2698: same at-rest flags pruneEmptyCategoryGroup sets on a
		// real category -- the bucket can hold hidden tiles only.
		u.HasButtons = u.VisibleButtons() > 0
		u.HasVisible = u.HasButtons
		roots = append(roots, u)
	}
	return roots
}

// inHiddenCategorySubtree reports whether catID or one of its ancestors is
// sell-screen hidden. A seen-set bounds the walk on malformed (cyclic)
// data, like isCategoryAncestor.
func inHiddenCategorySubtree(catID string, nodes map[string]data.CategoryNode) bool {
	seen := map[string]bool{}
	for cur := catID; cur != "" && !seen[cur]; {
		seen[cur] = true
		n, ok := nodes[cur]
		if !ok {
			return false
		}
		if n.SellScreenHidden {
			return true
		}
		cur = n.ParentID
	}
	return false
}

// dropHiddenGroups removes every group whose category is sell-screen
// hidden, recursively (a hidden group's subtree goes with it).
func dropHiddenGroups(groups []*CategoryGroup, nodes map[string]data.CategoryNode) []*CategoryGroup {
	kept := groups[:0]
	for _, g := range groups {
		if nodes[g.ID].SellScreenHidden {
			continue
		}
		g.Children = dropHiddenGroups(g.Children, nodes)
		kept = append(kept, g)
	}
	return kept
}

// categoryPicture is what a category shows on the sale screen — one
// picture per category (ut-docs#2717, iconid.Resolve): an uploaded photo
// this till can serve, else its icon id (manage-shop catalog contract
// §0.12) drawn through the till's icon registry — a set icon beats a
// library tile an older till stored in image_path, and such a tile reads
// as its own icon id. An id the registry doesn't know, or a malformed
// value that arrived over sync, draws the neutral fallback glyph and never
// reaches the page as-is. Else nothing.
func categoryPicture(c data.CategoryNode) string {
	return CategoryThumb(c.ImagePath, c.Icon)
}

// CategoryThumb is categoryPicture for callers holding the two raw columns
// rather than a CategoryNode — the /categories list and the Designer's
// category-management rows (ut-docs#2699) draw the same picture the sale
// screen does as each row's leading visual. The icon value only ever
// reaches the page through iconid.AssetPath; "" means "no picture" and the
// caller falls back to the colour swatch, then a placeholder.
func CategoryThumb(imagePath, icon string) string {
	path, id := iconid.Resolve(imagePath, icon)
	if img := categoryImageURL(path); img != "" {
		return img
	}
	return categoryImageURL(iconid.AssetPath(id))
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

// stampEditing (ut-docs#2174) is stampLocked's twin for ButtonVM.Editing —
// see that field's doc comment. A separate pass for the same reason
// stampLocked is one: the existing BuildCategoryGroups call sites (and their
// tests) know nothing about the Designer and keep compiling unchanged.
func stampEditing(groups []*CategoryGroup, editing bool) {
	for _, g := range groups {
		for i := range g.Buttons {
			g.Buttons[i].Editing = editing
		}
		stampEditing(g.Children, editing)
	}
}

// countButtonsPerCategory (ut-docs#2174) walks the tree BuildCategoryGroups
// returned and reports how many quick buttons each real category (by ID;
// the synthetic uncategorized bucket has none) directly holds — what the
// Designer's category-management list shows next to each name, so a manager
// can see at a glance which categories currently have quick buttons on the
// sale screen. Since ut-docs#2498, zero here does NOT imply the category is
// absent from the strip — it may still appear (with the buttons.html
// empty-state message) via an active-item count instead; a category is
// pruned from the strip entirely only when it has neither.
func countButtonsPerCategory(groups []*CategoryGroup) map[string]int {
	counts := map[string]int{}
	var walk func([]*CategoryGroup)
	walk = func(gs []*CategoryGroup) {
		for _, g := range gs {
			if g.ID != "" {
				// ut-docs#2698: visible tiles only -- a hidden tile is in the
				// render (greyed in edit mode) but not "on the sale screen".
				counts[g.ID] += g.VisibleButtons()
			}
			walk(g.Children)
		}
	}
	walk(groups)
	return counts
}

// DesignerCategoryVM (ut-docs#2174) is one row of the Designer's
// category-management list (buttons.html's "designer-categories" section,
// edit mode only): EVERY category — active or not, with or without quick
// buttons — unlike CategoryGroup, which only ever carries the active
// categories that have at least one button (the sale screen's own view).
// Color is the STORED colour ("" when none is set; the picker's "no colour"
// radio), not the auto-resolved swatch the strip falls back to.
type DesignerCategoryVM struct {
	ID          string
	Name        string
	Color       string
	IsActive    bool
	ItemCount   int // active catalog items in this category (blocks deactivation)
	ButtonCount int // quick buttons currently on the sale screen for it
	// Thumb (ut-docs#2699) is the row's leading picture: CategoryThumb of
	// the stored image/icon, "" when there is none (swatch, then
	// placeholder).
	Thumb string
}

// pruneEmptyCategoryGroup drops child branches with no buttons AND no
// active catalog items anywhere in their subtree (ut-docs#2498), and
// reports whether g itself still has any of either left. itemCounts[g.ID]
// is g's own DIRECT active-item count (a nil map, or an ID with no entry,
// reads as zero — Go's zero-value-on-missing-key rule — which is exactly
// "no items" and never panics). Also computes g.HasButtons: true when g
// OR ANY kept descendant has at least one quick button — seeded from g's
// own leaf check (len(g.Buttons) > 0) and OR'd with every child's already-
// computed HasButtons as the recursion unwinds, so it reflects the WHOLE
// kept subtree, not just g's own direct buttons. This is deliberately a
// different predicate from the survival test itself (hasAny): a group can
// now survive via itemCounts alone with zero buttons anywhere underneath,
// and the template needs to tell that case apart to render an empty-state
// message instead of an empty grid.
//
// ut-docs#2698: hidden tiles still keep a group alive (edit mode shows them
// greyed), but only visible ones set HasButtons; HasVisible is the at-rest
// "anything to show" test (a visible button, or itemCounts, which callers
// already fill with not-hidden item counts).
func pruneEmptyCategoryGroup(g *CategoryGroup, itemCounts map[string]int) bool {
	kept := g.Children[:0]
	hasAny := len(g.Buttons) > 0 || itemCounts[g.ID] > 0
	g.HasButtons = g.VisibleButtons() > 0
	g.HasVisible = g.HasButtons || itemCounts[g.ID] > 0
	for _, c := range g.Children {
		if pruneEmptyCategoryGroup(c, itemCounts) {
			kept = append(kept, c)
			hasAny = true
			if c.HasButtons {
				g.HasButtons = true
			}
			if c.HasVisible {
				g.HasVisible = true
			}
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
	// sellRepo backs the sell-screen tile cache's version and price-boundary
	// reads (ut-docs#2501, sellscreen_cache.go). nil (a hand-built
	// ButtonStore literal) = never cache.
	sellRepo *data.SellScreenRepo
}

func NewButtonStore(db *sql.DB) *ButtonStore {
	return &ButtonStore{
		repo:         data.NewShortcutsRepo(db),
		posRepo:      data.NewPOSRepo(db),
		modRepo:      data.NewModifierRepo(db),
		catalogRepo:  data.NewCatalogRepo(db),
		settingsRepo: data.NewSettingsRepo(db),
		sellRepo:     data.NewSellScreenRepo(db),
	}
}

// SellScreenVersion is the sell-screen tile cache's change marker
// (ut-docs#2501): ok=false means "don't cache this request".
func (s *ButtonStore) SellScreenVersion(ctx context.Context) (SellScreenVersion, bool, error) {
	if s.sellRepo == nil {
		return SellScreenVersion{}, false, nil
	}
	admin, sell, ok, err := s.sellRepo.SellScreenVersion(ctx)
	return SellScreenVersion{Admin: admin, Sell: sell}, ok, err
}

// NextPriceBoundary is the next scheduled price_history start/end — when a
// cached sell screen's prices go stale with no write (ut-docs#2501).
func (s *ButtonStore) NextPriceBoundary(ctx context.Context) (time.Time, error) {
	if s.sellRepo == nil {
		return time.Time{}, errors.New("sell screen repo not configured")
	}
	return s.sellRepo.NextPriceBoundary(ctx)
}

// CategoryTileVM (ut-docs#2499) is one tile of the category_tabs mode's
// grid, or one chip of the all_filter_chips mode's row: a top-level
// category with at least one ACTIVE catalog item anywhere in its subtree —
// quick button or not, which is what sets it apart from CategoryGroup (a
// quick-button surface). The synthetic uncategorized bucket has ID == "",
// same convention as CategoryGroup. ItemCount is the active-item count of
// the whole subtree, i.e. exactly what that tile's popup will list.
type CategoryTileVM struct {
	ID        string
	Name      string
	Color     string
	ItemCount int
	ImageURL  string // ut-docs#2500: see CategoryGroup.ImageURL
}

// BuildCategoryTiles derives the category_tabs/all_filter_chips category
// list from the SAME two inputs ButtonsHTTP.List already has in hand — the
// all-active item list (LoadAllActive) and the active category tree
// (LoadCategories) — by running them through BuildCategoryGroups: its
// pruning ("no active item anywhere in the subtree → no group"), its
// top-level-only roots, its resolved colours and its uncategorized bucket
// are precisely the rules these tiles need, so reusing it means the tile
// grid can never disagree with the strip about what a category is. No
// second query.
func BuildCategoryTiles(allActive []Button, cats []data.CategoryNode) []CategoryTileVM {
	groups := BuildCategoryGroups(allActive, cats, nil)
	out := make([]CategoryTileVM, 0, len(groups))
	for _, g := range groups {
		out = append(out, CategoryTileVM{ID: g.ID, Name: g.Name, Color: g.Color, ItemCount: countSubtreeButtons(g), ImageURL: g.ImageURL})
	}
	return out
}

func countSubtreeButtons(g *CategoryGroup) int {
	n := len(g.Buttons)
	for _, c := range g.Children {
		n += countSubtreeButtons(c)
	}
	return n
}

// filterButtonsInCategory keeps the buttons whose item sits in catID's
// subtree (catID itself or any descendant, walking each item's category up
// through the ACTIVE category tree the same way BuildCategoryGroups
// nests). catID == "" is the uncategorized bucket: an item with no
// category, or whose category no longer resolves in cats (an inactive or
// deleted one) — again exactly the bucket BuildCategoryGroups would put it
// in, so a tile/chip and its popup/grid always agree. Order is preserved.
func filterButtonsInCategory(buttons []Button, cats []data.CategoryNode, catID string) []Button {
	nodeByID := make(map[string]data.CategoryNode, len(cats))
	for _, c := range cats {
		nodeByID[c.ID] = c
	}
	inBucket := func(b Button) bool {
		_, resolves := nodeByID[b.CategoryID]
		if catID == "" {
			return b.CategoryID == "" || !resolves
		}
		if !resolves {
			return false
		}
		return b.CategoryID == catID || isCategoryAncestor(catID, b.CategoryID, nodeByID)
	}
	var out []Button
	for _, b := range buttons {
		if inBucket(b) {
			out = append(out, b)
		}
	}
	return out
}

// quickButtonsFirst orders a category's popup (ut-docs#2372's own
// acceptance wording): the category's quick buttons first, in their
// Designer order, then every remaining active item A–Z (allActive is
// already name-sorted by LoadAllActive). Matched by item id, so a quick
// button and its catalog row never render twice; a quick button whose
// item isn't in allActive (deactivated since) is dropped rather than shown
// unsellable.
func quickButtonsFirst(quick, allActive []Button) []Button {
	inAll := make(map[string]bool, len(allActive))
	for _, b := range allActive {
		inAll[b.ItemID] = true
	}
	out := make([]Button, 0, len(allActive))
	seen := make(map[string]bool, len(quick))
	for _, q := range quick {
		if q.ItemID == "" || seen[q.ItemID] || !inAll[q.ItemID] {
			continue
		}
		seen[q.ItemID] = true
		out = append(out, q)
	}
	for _, b := range allActive {
		if !seen[b.ItemID] {
			out = append(out, b)
		}
	}
	return out
}

// LoadCategories returns the flat category list the sale-screen grid nests
// buttons under (see BuildCategoryGroups). ListActiveCategories, not
// ListCategories (ut-docs#1898): a deactivated category must not offer
// itself as a sale-screen tab.
func (s *ButtonStore) LoadCategories(ctx context.Context) ([]data.CategoryNode, error) {
	return s.catalogRepo.ListActiveCategories(ctx)
}

// LoadCategoriesForAdmin (ut-docs#2174) is the Designer edit mode's
// counterpart to LoadCategories: every category, active AND inactive, with
// its active-item count — the same one query /categories renders its own
// admin list from (CatalogRepo.ListCategoriesForAdmin), so a category with
// no buttons (pruned from the sale-screen strip) or a deactivated one (which
// LoadCategories deliberately omits, ut-docs#1898) can still be managed.
func (s *ButtonStore) LoadCategoriesForAdmin(ctx context.Context) ([]data.CategoryAdminRow, error) {
	return s.catalogRepo.ListCategoriesForAdmin(ctx)
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
// an item with no quick button still shows on the sell screen
// (all_filter_chips' All grid, the category tiles/popups, and Load's
// implicit tiles).
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
//
// ut-docs#2698: this is the AT-REST set -- hidden and removed items are both
// left out. The grid's own load (loadAllActive, via Load/LoadWith) keeps the
// hidden ones, marked, for edit mode.
func (s *ButtonStore) LoadAllActive(ctx context.Context) ([]Button, error) {
	out, _, err := s.loadAllActive(ctx)
	return visibleOnly(out), err
}

// loadAllActive is LoadAllActive, also reporting whether any inner lookup
// failed and the tiles fell back (degraded) — the result is still returned
// and still rendered, but the sell-screen tile cache must not store a render
// built from it (ut-docs#2501 review finding 1). Unlike LoadAllActive it
// KEEPS hidden items, marked Button.Hidden (ut-docs#2698); removed items are
// left out here already.
func (s *ButtonStore) loadAllActive(ctx context.Context) (_ []Button, degraded bool, _ error) {
	items, err := s.catalogRepo.ListItems(ctx)
	if err != nil {
		return nil, false, err
	}
	// ut-docs#2541/#2698: an item removed from the quick buttons (items.
	// sell_screen_removed) is left out here -- no tile at rest or in edit
	// mode; one hidden from the sell screen (sell_screen_hidden) is KEPT and
	// marked (below), so the grid can show it greyed in edit mode, and the
	// at-rest callers drop it (visibleOnly). Both still sell via barcode
	// scan/live search (SearchSellable, the scan resolver), which don't call
	// this method. A lookup error is non-fatal-but-loud, same treatment as
	// every other batched lookup below: on error every item falls back to
	// VISIBLE (fails open, matching ListItems' own "every consumer that isn't
	// the sell screen must keep seeing hidden items unfiltered" contract)
	// rather than the render silently failing outright.
	hiddenIDs, removedIDs, err := s.catalogRepo.SellScreenStates(ctx)
	if err != nil {
		logging.L().Warnf("ui: load all-active items sell-screen-flag lookup failed, every item falls back to visible: %v", err)
		hiddenIDs, removedIDs = nil, nil
		degraded = true
	}
	if len(removedIDs) > 0 {
		kept := items[:0]
		for _, it := range items {
			if !removedIDs[it.ID] {
				kept = append(kept, it)
			}
		}
		items = kept
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
		degraded = true
	}
	thumbs, err := s.catalogRepo.ItemThumbnails(ctx)
	if err != nil {
		logging.L().Warnf("ui: load all-active items thumbnails failed, tiles fall back to no image: %v", err)
		degraded = true
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
				degraded = true
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
				degraded = true
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
				degraded = true
				continue
			}
			data.MergeMapInto(currentPrices, p)
		}
	}
	// ut-docs#2497: a tile's Code must round-trip through the exact same
	// /api/pos/scan resolver a tap posts it to. A raw barcode only
	// resolves there if it decodes under the shop's CURRENTLY ENABLED
	// symbologies (internal/data's enabledBarcodeSymbologies) — a barcode
	// stored under a symbology the shop has since disabled (or never
	// enabled) matches none of the resolver's tiers and taps fail with
	// "item not found" even though the item is active and listed right
	// here. Fetched once, shop-wide, above the loop (not per item) — same
	// swallow-the-error-and-default-safe treatment as
	// POSRepo.enabledBarcodeSymbologies: a settings read must never fail
	// this render (ADR-0003 offline-first). On error,
	// EnabledBarcodeSymbologies itself already returns
	// DefaultEnabledBarcodeSymbologyIDs() alongside the error, which is
	// exactly the set the resolver falls back to on the same error — so
	// using those defaults on error still keeps this check consistent with
	// what a tap will actually resolve against. The error only marks the
	// result degraded (a render from it is not cached, ut-docs#2501).
	enabledIDs, err := s.settingsRepo.EnabledBarcodeSymbologies(ctx)
	if err != nil {
		degraded = true
	}
	out := make([]Button, 0, len(items))
	for _, it := range items {
		rawBarcode := ""
		if bcs := barcodes[it.ID]; len(bcs) > 0 {
			rawBarcode = bcs[0] // ItemBarcodes orders primary first
		}
		code := resolvableTileCode(rawBarcode, it.SKU, it.ID, enabledIDs)
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
			Hidden:       hiddenIDs[it.ID],
		})
	}
	return out, degraded, nil
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
	// ut-docs#2497: same round-trip-with-the-scan-resolver requirement as
	// LoadAllActive above — see its comment for the full rationale. Fetched
	// once, shop-wide, above the loop.
	enabledIDs, _ := s.settingsRepo.EnabledBarcodeSymbologies(ctx)
	// ut-docs#2698: which results are not visible quick buttons (hidden or
	// removed) -- the search then offers "Add to quick buttons" on them in
	// edit mode. A lookup failure only loses that offer, never a result.
	var hiddenIDs, removedIDs map[string]bool
	if s.catalogRepo != nil {
		hiddenIDs, removedIDs, err = s.catalogRepo.SellScreenStates(ctx)
		if err != nil {
			logging.L().Warnf("ui: search-sellable sell-screen-flag lookup failed, no result offers add-to-quick-buttons: %v", err)
		}
	}
	out := make([]Button, 0, len(results))
	for _, r := range results {
		code := resolvableTileCode(r.Barcode, r.SKU, r.ItemID, enabledIDs)
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
			Hidden:       hiddenIDs[r.ItemID],
			Removed:      removedIDs[r.ItemID],
		})
	}
	return out, nil
}

// Load is LoadWith with a freshly-fetched LoadAllActive result -- the
// convenience most callers want. ButtonsHTTP.List needs the SAME
// LoadAllActive result a second time (for the All grid / category tiles),
// so it calls
// LoadAllActive itself and passes it to LoadWith directly rather than going
// through Load, which would otherwise run that batched, chunked query a
// second time on every single render (ut-docs#2541 review finding 2).
//
// ut-docs#2698: the GRID set -- hidden tiles included, marked Button.Hidden,
// in their own spot. UpdateOrder compares the posted order against this, and
// the rendered grid (ButtonVM.Pos) is indexed by it, so both must agree on
// hidden tiles being present.
func (s *ButtonStore) Load() ([]Button, error) {
	ctx := context.Background()
	allActive, _, err := s.loadAllActive(ctx)
	if err != nil {
		// Non-fatal-but-loud, same shape LoadWith's own doc comment
		// describes for this same lookup: the explicit rows still render,
		// the sell screen just loses every implicit tile until the next
		// successful Load.
		logging.L().Warnf("ui: load all-active items for implicit quick buttons failed, sell screen falls back to explicit shortcut_buttons rows only: %v", err)
		allActive = nil
	}
	return s.LoadWith(ctx, allActive)
}

// LoadWith is Load's own logic, taking an ALREADY-FETCHED LoadAllActive
// result (ut-docs#2541 review finding 2) instead of calling LoadAllActive
// itself -- ButtonsHTTP.List needs that same result again for its
// mode-specific views, and before this the render called LoadAllActive
// twice: once inside Load's old body, once directly for the (since
// ut-docs#2613 strip-less) All tab. Both are now the SAME batched,
// chunked query set (ItemBarcodes/ItemThumbnails/ItemIDsWithModifiers/
// ItemIDsWithVariants/ItemCurrentPrices, all sized to the WHOLE active
// catalog), so a large catalog paid for it twice on every render. allActive
// may be nil (a caller's own LoadAllActive failed and already logged it) --
// Load() above still returns explicit rows only in that case, matching the
// old behavior exactly.
func (s *ButtonStore) LoadWith(ctx context.Context, allActive []Button) ([]Button, error) {
	out, _, err := s.loadWith(ctx, allActive)
	return out, err
}

// loadWith is LoadWith, also reporting whether any of its own lookups failed
// and the tiles fell back (degraded) — see loadAllActive.
func (s *ButtonStore) loadWith(ctx context.Context, allActive []Button) (_ []Button, degraded bool, _ error) {
	// ut-docs#2698: LoadGridButtons, not LoadButtons -- a hidden item's
	// explicit row stays in the grid (marked Hidden) so it keeps its spot.
	rows, err := s.repo.LoadGridButtons(ctx)
	if err != nil {
		return nil, false, err
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
			degraded = true
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
			degraded = true
		}
		currentPrices, err = s.catalogRepo.ItemCurrentPrices(ctx, itemIDs)
		if err != nil {
			// Same non-fatal-but-loud treatment as hasVariants above
			// (ut-docs#2258): on this error every tile falls back to the
			// STALE configured base_price it already carries in b.Price,
			// same failure shape as ut-docs#2209's own hasVariants gap.
			logging.L().Warnf("ui: load item current prices failed, every tile falls back to raw base_price (ut-docs#2258): %v", err)
			degraded = true
		}
	}
	var out []Button
	seen := make(map[string]bool, len(rows))
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
			QuickButton:  true,
			Hidden:       b.Hidden,
		})
		if b.ItemID != "" {
			seen[b.ItemID] = true
		}
	}
	// ut-docs#2541: every other active, not-hidden catalog item is a quick
	// button too, by default — appended after the explicit shortcut_buttons
	// rows above (which keep their own sort_order), in LoadAllActive's own
	// order (by name), and reusing its exact code/price/thumbnail/mods/
	// variants logic rather than a second implementation of it. An explicit
	// row always wins for an item that has one (deduped via seen, built
	// above). ut-docs#2698: allActive comes from loadAllActive, which keeps
	// hidden items marked Hidden (edit mode shows them greyed, in their
	// name-ordered spot) and has already dropped removed ones. allActive is the caller's own
	// already-fetched result (see this method's own doc comment) — a nil
	// slice (the caller's LoadAllActive failed, already logged there) simply
	// contributes nothing, same fallback shape the old inline call had.
	for _, b := range allActive {
		if seen[b.ItemID] {
			continue
		}
		out = append(out, b)
	}
	return out, degraded, nil
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
//
// ut-docs#2541: since every active, non-hidden item is now an IMPLICIT
// quick button (Load, above) even with no shortcut_buttons row of its own,
// a drag can reorder one of those tiles too — but ShortcutsRepo.UpdateOrder
// only ever UPDATEs an existing row's sort_order, so an implicit tile's new
// position would silently not persist. Any code in codes with no row yet
// AND at or before lastTouchedIndex (below) is materialised first.
//
// ut-docs#2541 review finding 1: the client (app.js's utTileJiggle) always
// posts the FULL global code list on every drag, not just the tiles that
// moved — materializing every implicit code in that list, unconditionally,
// would turn the very first drag on a large catalog into a real
// shortcut_buttons row for EVERY implicit item, most of which the operator
// never touched (thousands of inserts, frozen labels/images, and a bloated
// cloud heartbeat report — remoteQuickButtonsReport reads LoadButtons).
// lastTouchedIndex is the LAST position where codes differs from Load()'s
// CURRENT order; only codes at or before it are candidates for
// materializing — a trailing run of tiles the drag never actually reordered
// (same code, same position, both before and after) stays implicit. The
// existence check (ExistingBarcodes) and every insert/order-update run in
// ONE transaction (ShortcutsRepo.MaterializeAndReorder) rather than one
// transaction per implicit tile plus a separate reorder transaction.
func (s *ButtonStore) UpdateOrder(ctx context.Context, codes []string) error {
	current, err := s.Load()
	if err != nil {
		return err
	}
	lastTouchedIndex := -1
	for i, code := range codes {
		var currentCode string
		if i < len(current) {
			currentCode = current[i].Code
		}
		if code != currentCode {
			lastTouchedIndex = i
		}
	}

	existing, err := s.repo.ExistingBarcodes(ctx, codes)
	if err != nil {
		return err
	}
	var materialize []data.ShortcutButton
	for i, code := range codes {
		if i > lastTouchedIndex {
			// Trailing run of tiles the drag never touched (same code, same
			// position, before and after) -- leave any implicit one among
			// them implicit; only the reorder UPDATE below still runs for
			// it (a no-op UPDATE, since its position didn't change either).
			break
		}
		if existing[code] {
			continue
		}
		line, _, ok := s.posRepo.ResolveShortcutLineDecoded(ctx, code)
		if !ok || line.ItemID == "" {
			// An unresolvable code (stale/tampered client state) simply has
			// nothing to materialise -- the UPDATE below finds no row to set
			// sort_order on for it either, the same silent no-op this route
			// already had for an unknown code before this card.
			continue
		}
		// Label is deliberately left EMPTY, not line.Label/line.Name: a
		// materialized row must show the item's LIVE name (and thumbnail),
		// never one frozen at drag time — see LoadButtons' own
		// COALESCE(NULLIF(sb.label,''), i.name) fallback, and
		// MaterializeAndReorder's own doc comment. ImageURL is left empty
		// for the same reason: LoadButtons already falls a NULL image_path
		// back to the item's own catalog thumbnail.
		materialize = append(materialize, data.ShortcutButton{
			Label:   "",
			Barcode: code,
			ItemID:  line.ItemID,
		})
	}
	return s.repo.MaterializeAndReorder(ctx, materialize, codes)
}

// synthesizedButtonCodePrefix marks a shortcut-button code that ButtonStore.Add
// generated itself (ut-docs#1459) rather than one carrying a real barcode or
// SKU — see Add below. Never a real barcode/SKU value, so it's a safe
// signal for anywhere downstream that must not present it as if it were
// one (PriceResolverAdapter.resolve blanks it off the basket line's SKU
// rather than let a raw item UUID reach a receipt or the journal).
const synthesizedButtonCodePrefix = "item:"

// resolvableTileCode picks the Code a sell-screen tile (LoadAllActive /
// SearchSellable) should carry so that tapping it always round-trips
// through /api/pos/scan → POSRepo.ResolveShortcutLineDecoded (ut-docs#2497).
// rawBarcode is used ONLY when it actually decodes under the shop's
// currently-enabled barcode symbologies (enabledIDs) — the exact same test
// ResolveScanLine applies via barcode.Default().Match. A barcode stored
// under a symbology the shop doesn't currently have enabled (catalog
// import predates a settings change, etc.) resolves under NONE of the
// scan resolver's tiers, so it must be treated exactly like "no barcode at
// all" and fall through to the SKU tier, then to the synthesized
// itemIDCodePrefix ("item:"+id) tier. Both fall-through tiers resolve
// regardless of symbology settings — the itemIDCodePrefix tier
// unconditionally so; the SKU tier ordinarily too, though (unchanged by
// this fix, and pre-existing for any item with no barcode at all) a SKU
// happens to collide with a customer/loyalty/voucher code prefix the scan
// handler intercepts first, or with another item's own resolvable
// barcode, it can resolve to something other than this item. Both tiers
// are already covered by existing tests.
func resolvableTileCode(rawBarcode, sku, itemID string, enabledIDs []string) string {
	if rawBarcode != "" {
		if _, ok := barcode.Default().Match(enabledIDs, rawBarcode); ok {
			return rawBarcode
		}
	}
	if sku != "" {
		return sku
	}
	return synthesizedButtonCodePrefix + itemID
}

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
	// ut-docs#2541/#2698: AddButton reuses the item's existing row (its
	// position) when it has one, and clears both sell-screen flags, in one
	// transaction -- adding a tile for a hidden or removed item puts it back.
	ctx := context.Background()
	if err := s.repo.AddButton(ctx, data.ShortcutButton{
		Label:    btn.Label,
		Barcode:  btn.Code,
		ItemID:   btn.ItemID,
		ImageURL: btn.ImageURL,
	}); err != nil {
		return err
	}
	return nil
}

// Remove is the legacy /api/buttons/remove's store call: it removes the item
// behind code from the quick buttons (ut-docs#2698 -- the same thing the
// trash badge does, RemoveFromQuickButtons; before, #2541 made it a hide).
// itemID, when the caller already has it, is used as-is; otherwise it's
// resolved from code's own shortcut_buttons row, so a caller with only the
// legacy code payload (an external API caller) still works.
func (s *ButtonStore) Remove(code, itemID string) error {
	ctx := context.Background()
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		code = strings.TrimSpace(code)
		if code == "" {
			return errors.New("code or itemId is required")
		}
		resolved, ok := s.repo.ItemIDForBarcode(ctx, code)
		if !ok {
			return errors.New("item not found for code")
		}
		itemID = resolved
	}
	return s.catalogRepo.RemoveFromSellScreen(ctx, itemID)
}

// Hide takes an item off the sell-screen quick-button grid and the
// all_filter_chips All grid at rest (ut-docs#2541) — see
// CatalogRepo.SetSellScreenHidden for the full contract (still sells via
// scan/search; ut-docs#2698: keeps any explicit tile row, so the tile shows
// greyed in its own spot while the grid is being edited).
func (s *ButtonStore) Hide(ctx context.Context, itemID string) error {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return errors.New("itemId is required")
	}
	return s.catalogRepo.SetSellScreenHidden(ctx, itemID, true)
}

// Unhide puts a previously hidden item back on the sell-screen grid
// (ut-docs#2541) — in the same spot it was hidden in (ut-docs#2698: its
// explicit row, if it had one, was kept), or as an IMPLICIT tile if it never
// had a row. A removed item stays removed (Unhide only clears hidden).
func (s *ButtonStore) Unhide(ctx context.Context, itemID string) error {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return errors.New("itemId is required")
	}
	return s.catalogRepo.SetSellScreenHidden(ctx, itemID, false)
}

// UnhideAll puts every hidden active item back on the sell-screen grid in
// one step (ut-docs#2614 — the Designer's "Show all N on the sell screen")
// and returns how many it unhid. Like Unhide, each comes back as an
// IMPLICIT tile — no shortcut_buttons rows are written.
func (s *ButtonStore) UnhideAll(ctx context.Context) (int, error) {
	return s.catalogRepo.UnhideAllSellScreen(ctx)
}

// ListHidden returns every item currently hidden from the sell screen — the
// Designer's "Hidden from sell screen" section (ut-docs#2541).
func (s *ButtonStore) ListHidden(ctx context.Context) ([]data.HiddenItem, error) {
	return s.catalogRepo.ListSellScreenHidden(ctx)
}

// RemoveFromQuickButtons is the trash badge's store call (ut-docs#2698):
// the item stops being a quick button -- absent from the grid at rest and in
// edit mode -- while the catalog item stays active and keeps selling by
// scan/search; Add (from search) brings it back. It never deactivates the
// item: that stays in the catalog editor. See CatalogRepo.RemoveFromSellScreen.
func (s *ButtonStore) RemoveFromQuickButtons(ctx context.Context, itemID string) error {
	itemID = strings.TrimSpace(itemID)
	if itemID == "" {
		return errors.New("itemId is required")
	}
	return s.catalogRepo.RemoveFromSellScreen(ctx, itemID)
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
	// BrowsingMode (ut-docs#2499) is the shop's sale.browsing_mode — one
	// of common.BrowsingModeCategoryTabs/AllFilterChips/StripOverflow,
	// passed already clamped by internal/pages/buttons_api.go (internal/ui
	// can't import internal/pages/common — Deps.BtnStore is a *ButtonStore,
	// so that would be a cycle — which is why the string literals below
	// are repeated here rather than referenced; buttons_browsing_mode_test.go
	// pins all three). The Go zero value "" renders the strip: every
	// pre-#2499 &ButtonsHTTP{Store: ..., View: ...} literal in this
	// package's tests was written against the strip, and the fragment
	// handlers (AllMore/Search/CategoryItems) never render a mode at all —
	// the "zero value keeps the historical shape" convention. NOT the
	// setting's own default (that
	// is category_tabs, common.DefaultBrowsingMode): the one production
	// caller always passes the live clamped value, so the zero value is
	// only ever reachable from code that never wanted a mode.
	//
	// ut-docs#2613: the strip has no All tab any more — it shows category
	// tabs plus the ut-docs#2307 "…" button only. The one All grid left is
	// all_filter_chips' own; every active item is still reachable from the
	// strip through the server-side search (Search below).
	BrowsingMode string
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
	// EditMode (ut-docs#2174): render the Designer's live replica of the
	// sale screen instead of the sale screen itself — same "buttons"
	// template, with the category-management section added and the
	// sale-only affordances (search, plugin action strip, live scan tiles)
	// removed. Set by registerButtonsAPI's /ui/buttons handler
	// from ?mode=edit, which it only honours for a catalog_management
	// session (a cashier gets a 403, never this UI). Zero value = the sale
	// screen, so every existing ButtonsHTTP literal renders exactly as
	// before.
	EditMode bool
	// Cache (ut-docs#2501) serves List (outside EditMode) and CategoryItems
	// from rendered bytes while nothing they depend on has changed — see
	// sellscreen_cache.go for the invalidation contract. nil = no caching:
	// every existing ButtonsHTTP literal renders exactly as before.
	Cache *SellScreenCache
	// Locale is this request's resolved locale — the one the View's funcs
	// were built for. Only read as part of Cache's key.
	Locale string
}

// AllTabPageSize bounds how many of the sell screen's All-grid items
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
	// ut-docs#2501: the Designer's edit mode is never cached — it is a
	// manager's working surface (hidden-items list, category management),
	// rendered rarely, and must always show the live state it is editing.
	if h.EditMode {
		h.renderList(w, r)
		return
	}
	h.serveSellScreen(w, r, h.sellScreenKey("list", ""), h.renderList)
}

// renderList is List's render, reporting whether it is clean enough to cache
// (ut-docs#2501): every load below is non-fatal to the render, but a render
// that degraded around a failed load must not be served for minutes after.
func (h *ButtonsHTTP) renderList(w http.ResponseWriter, r *http.Request) bool {
	clean := true
	// ut-docs#2541 review finding 2: LoadAllActive is fetched exactly ONCE
	// per render and reused for both Load's implicit-tile merge AND the
	// mode-specific views below (all_filter_chips' All grid, the category
	// tiles), via LoadWith -- before this fix, Store.Load() ran its own
	// internal LoadAllActive call AND this handler ran a second, separate
	// one for the All grid, doubling the cost of LoadAllActive's own batched/
	// chunked queries (ItemBarcodes, ItemThumbnails, ItemIDsWithModifiers,
	// ItemIDsWithVariants, ItemCurrentPrices — all sized to the whole active
	// catalog) on every single /ui/buttons render. Load's own implicit merge
	// always needs the full active-item set, in every mode — including the
	// strip, which since ut-docs#2613 has no All grid of its own.
	// A degraded load (an inner lookup fell back, already logged) still
	// renders, but is not clean: it must not be cached (review finding 1).
	allBtns, degraded, err := h.Store.loadAllActive(r.Context())
	if err != nil {
		logging.L().Errorf("buttons list: load all-active items: %v", err)
	}
	if err != nil || degraded {
		clean = false
	}
	btns, degraded, err := h.Store.loadWith(r.Context(), allBtns)
	if err != nil {
		logging.L().Errorf("buttons list: load buttons: %v", err)
	}
	if err != nil || degraded {
		clean = false
	}
	cats, err := h.Store.LoadCategories(r.Context())
	if err != nil {
		// Not fatal to the render — every button still shows, just
		// ungrouped (BuildCategoryGroups buckets them as uncategorized)
		// — but worth a log line: a till stuck like this permanently
		// loses category grouping/coloring with no visible sign why.
		logging.L().Errorf("buttons list: load categories: %v", err)
		clean = false
	}
	// ut-docs#2499: which of the three sell-screen shapes to render. The
	// Designer's replica (EditMode) is always the quick-button strip — it
	// exists to arrange quick buttons, and neither the category popup
	// (index.html's modal, which the Designer page doesn't have) nor the
	// All grid is a quick-button surface. The zero value "" is the strip
	// too — see the field's own doc comment.
	mode := h.BrowsingMode
	if h.EditMode || mode == "" {
		mode = browsingModeStripOverflow
	}
	// ut-docs#2294: the all-active item list is loaded once per /ui/buttons
	// render (this handler is only ever fetched on page load and on
	// "modifiers-changed from:body" -- see buttons.html's own top comment
	// -- never on a basket mutation), not re-queried per basket change.
	// ut-docs#2541: allBtns is loaded unconditionally at the top of this
	// handler (Load's implicit-tile merge always needs it), so every mode
	// below — all_filter_chips' All grid and ut-docs#2499's
	// BuildCategoryTiles — reuses that ONE load rather than querying again.
	// The strip (the default branch) pages nothing: ut-docs#2613 retired its
	// All tab, so it renders quick-button categories only.
	var allPage []Button
	var allHasMore bool
	var categoryTiles []CategoryTileVM
	// ut-docs#2698: the category tiles and the All grid are at-rest browse
	// surfaces with no edit mode of their own, so hidden items stay out of
	// them (allBtns itself keeps them, marked, for the quick-button grid).
	switch mode {
	case browsingModeCategoryTabs:
		categoryTiles = BuildCategoryTiles(visibleOnly(allBtns), cats)
	case browsingModeAllFilterChips:
		visible := visibleOnly(allBtns)
		categoryTiles = BuildCategoryTiles(visible, cats)
		// ut-docs#2319: only the first page ships on the initial render
		// (and on every modifiers-changed/buttons-changed whole-document
		// refetch) — see AllTabPageSize's own doc comment. The rest loads
		// on demand via the "load more" button AllMore serves below.
		allPage, allHasMore = pageButtons(visible, 0)
	}
	// ut-docs#2498: LoadCategoriesForAdmin is the one query that carries a
	// per-category ACTIVE ITEM count (data.CategoryAdminRow.ItemCount) —
	// BuildCategoryGroups needs it too now, to keep a category with real
	// items but zero quick buttons from being pruned off the sale screen
	// entirely (previously it only ever survived pruning by having a
	// button somewhere in its subtree). Hoisted out of the `if h.EditMode`
	// block below (which used to be its only caller) to run unconditionally,
	// ONE query either way — the EditMode branch reuses these same rows to
	// build adminCats exactly as before, rather than querying twice.
	adminRows, err := h.Store.LoadCategoriesForAdmin(r.Context())
	if err != nil {
		// Same non-fatal-but-loud shape as the categories load above: the
		// render still proceeds, just with an empty item-count map (no
		// category survives pruning via item count alone this render) and,
		// in edit mode, without the management list.
		logging.L().Errorf("buttons list: load categories for admin: %v", err)
		clean = false
	}
	// ut-docs#2541 review finding 5: VisibleItemCount, not ItemCount — a
	// category whose active items are ALL hidden from the sell screen must
	// not keep surviving BuildCategoryGroups' pruning with an empty group.
	// DesignerCategoryVM below still shows the admin the unfiltered
	// ItemCount (a manager managing categories needs to see hidden items
	// too), so only THIS map, which exists purely to drive pruning, changes.
	itemCounts := make(map[string]int, len(adminRows))
	for _, c := range adminRows {
		itemCounts[c.ID] = c.VisibleItemCount
	}
	groups := BuildCategoryGroups(btns, cats, itemCounts)
	stampLocked(groups, h.Granted)
	stampEditing(groups, h.EditMode)
	// ut-docs#2698: does anything show AT REST? With every quick button
	// hidden the sale screen renders its empty state (nothing to tap); the
	// Designer never does (buttons.html).
	anyVisible := false
	for _, g := range groups {
		if g.HasVisible {
			anyVisible = true
			break
		}
	}
	// ut-docs#2174: the Designer's category-management list. Only loaded
	// in edit mode, so the sale screen pays nothing for it.
	var adminCats []DesignerCategoryVM
	var palette []catalogtypes.ItemColor
	// ut-docs#2541: the Designer's "Hidden from sell screen" section — only
	// built in EditMode, same reasoning as adminCats/palette just above: the
	// sale screen itself has no use for the list and pays nothing for it.
	var hidden []data.HiddenItem
	if h.EditMode {
		counts := countButtonsPerCategory(groups)
		adminCats = make([]DesignerCategoryVM, 0, len(adminRows))
		for _, c := range adminRows {
			adminCats = append(adminCats, DesignerCategoryVM{
				ID:          c.ID,
				Name:        c.Name,
				Color:       c.Color,
				IsActive:    c.IsActive,
				ItemCount:   c.ItemCount,
				ButtonCount: counts[c.ID],
				Thumb:       CategoryThumb(c.ImagePath, c.Icon),
			})
		}
		palette = catalogtypes.ItemColors()
		hidden, err = h.Store.ListHidden(r.Context())
		if err != nil {
			// Non-fatal-but-loud, same shape as the lookups above: the
			// Designer still renders, just with an empty (rather than
			// possibly-stale) hidden-items section until the next successful
			// render.
			logging.L().Errorf("buttons list: load hidden items: %v", err)
			clean = false
		}
	}
	if err := h.View.Render(w, "buttons", map[string]any{
		"Groups":          groups,
		"AllButtons":      ToVM(allPage),
		"AllHasMore":      allHasMore,
		"AllNextOffset":   len(allPage),
		"BrowsingMode":    mode,
		"CategoryTiles":   categoryTiles,
		"EditMode":        h.EditMode,
		"AdminCategories": adminCats,
		"ItemColors":      palette,
		"HiddenItems":     hidden,
		"AnyVisible":      anyVisible,
	}); err != nil {
		logging.L().Warnf("buttons list: render: %v", err)
		clean = false
	}
	return clean
}

// The three sale.browsing_mode values, as this package must spell them
// (see ButtonsHTTP.BrowsingMode for why they aren't referenced from
// internal/pages/common). buttons_browsing_mode_test.go and
// internal/pages/buttons_api_test.go together pin that these three literals
// and common.BrowsingMode* never drift apart.
const (
	browsingModeCategoryTabs   = "category_tabs"
	browsingModeAllFilterChips = "all_filter_chips"
	browsingModeStripOverflow  = "strip_overflow"
)

// AllMore renders the next page of the sell screen's All grid (ut-docs#2319)
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
//
// ut-docs#2499: the all_filter_chips mode's chips call this same route
// with ?category=<id>&offset=0 to swap the grid to that category's subtree
// (filterButtonsInCategory — nested categories fold into their top-level
// chip, "" is the uncategorized bucket), and the page's own load-more
// button carries the category along so paging stays inside the filter. No
// ?category, or ?category=all, is the whole catalog (the chip row's All
// chip sends category=all; the strip's own All tab, which sent no
// category, was retired by ut-docs#2613).
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
	category, filtered := r.URL.Query().Get("category"), false
	if _, has := r.URL.Query()["category"]; has && category != "all" {
		cats, err := h.Store.LoadCategories(r.Context())
		if err != nil {
			logging.L().Errorf("buttons all-more: load categories: %v", err)
		}
		all = filterButtonsInCategory(all, cats, category)
		filtered = true
	}
	page, hasMore := pageButtons(all, offset)
	_ = h.View.Render(w, "all-more-fragment", map[string]any{
		"Buttons":    ToVM(page),
		"HasMore":    hasMore,
		"NextOffset": offset + len(page),
		"Category":   category,
		"Filtered":   filtered,
	})
}

// CategoryItems renders the category_tabs mode's popup body (ut-docs#2499,
// absorbing ut-docs#2372): EVERY active item in the category's subtree —
// the category's quick buttons first, in their Designer order, then the
// rest A–Z (quickButtonsFirst) — as the same sellable tile a search result
// uses, plus the popup's own client-side search box (buttons.html's
// "category-items-fragment"). ?id= is the category (""/absent = the
// uncategorized bucket, same convention as BuildCategoryGroups); an id that
// no longer resolves renders an empty popup, never an error. Rendered on
// open, per tap — the tiles are never a second always-present copy in the
// DOM (#2372's strict-mode-locator requirement, and why the ut-docs#2283
// clone-the-panel picker was retired with the Categories tab itself).
//
// ut-docs#2501: served from h.Cache (keyed on the category id) while nothing
// it depends on has changed, and LoadAllActive runs once per render — the
// quick-button half reuses it via LoadWith, as List does, instead of Load()
// running its own second LoadAllActive.
func (h *ButtonsHTTP) CategoryItems(w http.ResponseWriter, r *http.Request) {
	catID := r.URL.Query().Get("id")
	h.serveSellScreen(w, r, h.sellScreenKey("category", catID), func(w http.ResponseWriter, r *http.Request) bool {
		clean := true
		all, degraded, err := h.Store.loadAllActive(r.Context())
		if err != nil {
			logging.L().Errorf("buttons category items: load all-active items: %v", err)
		}
		if err != nil || degraded {
			clean = false
		}
		cats, err := h.Store.LoadCategories(r.Context())
		if err != nil {
			logging.L().Errorf("buttons category items: load categories: %v", err)
			clean = false
		}
		quick, degraded, err := h.Store.loadWith(r.Context(), all)
		if err != nil {
			logging.L().Errorf("buttons category items: load quick buttons: %v", err)
		}
		if err != nil || degraded {
			clean = false
		}
		// ut-docs#2698: the popup is an at-rest browse surface -- hidden
		// items (kept, marked, by the grid loads) stay out of it.
		items := quickButtonsFirst(
			filterButtonsInCategory(visibleOnly(quick), cats, catID),
			filterButtonsInCategory(visibleOnly(all), cats, catID),
		)
		if err := h.View.Render(w, "category-items-fragment", map[string]any{
			"Buttons": ToVM(items),
		}); err != nil {
			logging.L().Warnf("buttons category items: render: %v", err)
			clean = false
		}
		return clean
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
	// ut-docs#2174: nothing to render. This used to re-render the
	// Designer's own flat admin grid (buttons_admin.html's
	// "buttons_admin_grid") for htmx to swap in; the Designer is now a live
	// replica of the sale screen (GET /ui/buttons?mode=edit) that refreshes
	// itself off the HX-Trigger above, exactly as the sale screen does, so
	// the retired grid is gone and an empty 200 is the whole success
	// response. (The Designer's search-result button targets
	// #buttons-add-error with innerHTML, so this empty body also clears any
	// earlier refusal shown there.)
	w.WriteHeader(http.StatusOK)
	return true
}

// Remove returns whether the item was actually removed from the quick
// buttons -- ut-docs#2358, same rationale as Add's own doc comment above:
// false on every early-return, true only once Store.Remove has actually
// succeeded. ut-docs#2698: the legacy route now does what the trash badge
// does (ButtonStore.Remove -> RemoveFromSellScreen); #2541 had made it a
// hide.
func (h *ButtonsHTTP) Remove(w http.ResponseWriter, r *http.Request) bool {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return false
	}
	if err := h.Store.Remove(r.Form.Get("code"), r.Form.Get("itemId")); err != nil {
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
	// ut-docs#2174: empty 200, same reasoning as Add above — the retired
	// flat admin grid this used to re-render no longer exists anywhere.
	w.WriteHeader(http.StatusOK)
	return true
}

// buttonsItemIDForm parses the request and returns the trimmed "itemId"
// form value, writing a localized 400 fragment (same shape as Add/Remove's
// own store-error response above) and returning ok=false for a missing
// value or a form-parse failure — Hide/Unhide/RemoveFromQuickButtons below share this
// exact validation, unlike Add/Remove which each need their own.
func buttonsItemIDForm(w http.ResponseWriter, r *http.Request) (itemID string, ok bool) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return "", false
	}
	itemID = strings.TrimSpace(r.Form.Get("itemId"))
	if itemID == "" {
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<div class="error">` + html.EscapeString(httpx.T(locale, designerErrorServerKey)) + `</div>`))
		return "", false
	}
	return itemID, true
}

// Hide returns whether the item was actually hidden -- ut-docs#2358/#2541,
// same false-only-on-failure/true-on-success contract as Add/Remove.
func (h *ButtonsHTTP) Hide(w http.ResponseWriter, r *http.Request) bool {
	itemID, ok := buttonsItemIDForm(w, r)
	if !ok {
		return false
	}
	if err := h.Store.Hide(r.Context(), itemID); err != nil {
		logging.L().Infof("[buttons] hide: %v", err)
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<div class="error">` + html.EscapeString(httpx.T(locale, designerErrorServerKey)) + `</div>`))
		return false
	}
	w.Header().Set("HX-Trigger", "buttons-changed")
	w.WriteHeader(http.StatusOK)
	return true
}

// Unhide returns whether the item was actually unhidden -- ut-docs#2358/#2541.
func (h *ButtonsHTTP) Unhide(w http.ResponseWriter, r *http.Request) bool {
	itemID, ok := buttonsItemIDForm(w, r)
	if !ok {
		return false
	}
	if err := h.Store.Unhide(r.Context(), itemID); err != nil {
		logging.L().Infof("[buttons] unhide: %v", err)
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<div class="error">` + html.EscapeString(httpx.T(locale, designerErrorServerKey)) + `</div>`))
		return false
	}
	w.Header().Set("HX-Trigger", "buttons-changed")
	w.WriteHeader(http.StatusOK)
	return true
}

// UnhideAll returns how many items it unhid and whether it succeeded --
// ut-docs#2614. Same false-only-on-failure contract as Unhide; the count is
// for the caller's audit row. It takes no form input.
func (h *ButtonsHTTP) UnhideAll(w http.ResponseWriter, r *http.Request) (int, bool) {
	n, err := h.Store.UnhideAll(r.Context())
	if err != nil {
		logging.L().Infof("[buttons] unhide-all: %v", err)
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<div class="error">` + html.EscapeString(httpx.T(locale, designerErrorServerKey)) + `</div>`))
		return 0, false
	}
	w.Header().Set("HX-Trigger", "buttons-changed")
	w.WriteHeader(http.StatusOK)
	return n, true
}

// RemoveFromQuickButtons returns whether the item was actually removed from
// the quick buttons -- ut-docs#2698: the jiggle-mode trash badge's target
// (see ButtonStore.RemoveFromQuickButtons). It never deactivates the item.
func (h *ButtonsHTTP) RemoveFromQuickButtons(w http.ResponseWriter, r *http.Request) bool {
	itemID, ok := buttonsItemIDForm(w, r)
	if !ok {
		return false
	}
	if err := h.Store.RemoveFromQuickButtons(r.Context(), itemID); err != nil {
		logging.L().Infof("[buttons] remove-from-grid: %v", err)
		locale := httpx.ResolveLocale(w, r)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`<div class="error">` + html.EscapeString(httpx.T(locale, designerErrorServerKey)) + `</div>`))
		return false
	}
	w.Header().Set("HX-Trigger", "buttons-changed")
	w.WriteHeader(http.StatusOK)
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
