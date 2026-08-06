package ui

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"hash/fnv"
	"html/template"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/money"
	pos "github.com/universaltill/universal-till/internal/pos"
	uiassets "github.com/universaltill/universal-till/web"
)

// Button represents a shortcut button backed by the shortcut_buttons table.
type Button struct {
	Label        string `json:"label"`
	Code         string `json:"code"` // barcode/PLU associated with the shortcut
	ItemID       string `json:"itemId"`
	ImageURL     string `json:"imageUrl,omitempty"`
	Price        int64  `json:"price,omitempty"` // minor units, display only
	HasModifiers bool   `json:"hasModifiers,omitempty"`
	CategoryID   string `json:"categoryId,omitempty"` // the item's category, empty when uncategorized
}

// ButtonVM is the view-model passed to templates.
type ButtonVM struct {
	Label        string `json:"label"`
	Code         string `json:"code"`
	ItemID       string `json:"itemId"`
	ImageURL     string `json:"imageUrl,omitempty"`
	Price        int64  `json:"price,omitempty"` // minor units, display only
	HasModifiers bool   `json:"hasModifiers,omitempty"`
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
	for _, b := range buttons {
		g, ok := byID[b.CategoryID]
		if b.CategoryID == "" || !ok {
			uncategorized = append(uncategorized, toButtonVM(b))
			continue
		}
		g.Buttons = append(g.Buttons, toButtonVM(b))
	}

	kept := roots[:0]
	for _, g := range roots {
		if pruneEmptyCategoryGroup(g) {
			kept = append(kept, g)
		}
	}
	roots = kept

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
	repo        *data.ShortcutsRepo
	posRepo     *data.POSRepo
	modRepo     *data.ModifierRepo
	catalogRepo *data.CatalogRepo
}

func NewButtonStore(db *sql.DB) *ButtonStore {
	return &ButtonStore{
		repo:        data.NewShortcutsRepo(db),
		posRepo:     data.NewPOSRepo(db),
		modRepo:     data.NewModifierRepo(db),
		catalogRepo: data.NewCatalogRepo(db),
	}
}

// LoadCategories returns the flat category list the sale-screen grid nests
// buttons under (see BuildCategoryGroups).
func (s *ButtonStore) LoadCategories(ctx context.Context) ([]data.CategoryNode, error) {
	return s.catalogRepo.ListCategories(ctx)
}

type SearchResult struct {
	ItemID  string
	Name    string
	Barcode string
	Image   string
}

// AddVals returns the JSON payload the Designer's search-result button
// posts to /api/buttons/add (htmx parses the hx-vals attribute with
// JSON.parse). Marshaled server-side so a name/barcode/path containing a
// double quote or backslash survives the HTML-attribute round trip —
// interpolating the raw fields into a JSON literal inside the template
// produced invalid JSON for any quoted name, silently breaking add.
func (r SearchResult) AddVals() string {
	b, _ := json.Marshal(map[string]string{
		"label":    r.Name,
		"code":     r.Barcode,
		"itemId":   r.ItemID,
		"imageUrl": r.Image,
	})
	return string(b)
}

// SearchItems finds items (and primary barcodes) to add as shortcuts.
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
			Image:   r.Image,
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
		hasMods, _ = s.modRepo.ItemIDsWithModifiers(ctx, itemIDs)
	}
	var out []Button
	for _, b := range rows {
		out = append(out, Button{
			Label:        b.Label,
			Code:         b.Barcode,
			ItemID:       b.ItemID,
			ImageURL:     b.ImageURL,
			Price:        b.Price,
			HasModifiers: hasMods[b.ItemID],
			CategoryID:   b.CategoryID,
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

func (s *ButtonStore) Add(btn Button) error {
	btn.Label = strings.TrimSpace(btn.Label)
	btn.Code = strings.TrimSpace(btn.Code)
	btn.ItemID = strings.TrimSpace(btn.ItemID)
	if btn.Label == "" || btn.Code == "" || btn.ItemID == "" {
		return errors.New("label, code, and itemId are required")
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

func NewRenderer(layout, page, partial string, funcs template.FuncMap) (*Renderer, error) {
	// Parse templates with provided funcs (includes T for i18n)
	t, err := template.New("base.html").Funcs(funcs).ParseFS(uiassets.FS,
		stripWebPrefix(layout),
		stripWebPrefix(page),
		"ui/partials/nav.html",
		// base.html references it on every page; must be parsed alongside
		// the layout or executing "base" fails.
		"ui/partials/bugreport_panel.html",
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
	_ = h.View.Render(w, "buttons", map[string]any{
		"Groups": BuildCategoryGroups(btns, cats),
	})
}

func (h *ButtonsHTTP) Add(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
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
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	// Re-render admin grid so htmx swaps only the grid in designer
	btns, _ := h.Store.Load()
	_ = h.View.Render(w, "buttons_admin_grid", map[string]any{
		"Buttons": ToVM(btns),
	})
}

func (h *ButtonsHTTP) Remove(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := h.Store.Remove(r.Form.Get("code")); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	btns, _ := h.Store.Load()
	_ = h.View.Render(w, "buttons_admin_grid", map[string]any{
		"Buttons": ToVM(btns),
	})
}

type PriceResolverAdapter struct{ Store *ButtonStore }

func (a PriceResolverAdapter) Resolve(code string) (pos.BasketLine, bool) {
	ctx := context.Background()

	if line, ok := a.resolveVariant(ctx, code); ok {
		return line, true
	}
	if line, ok := a.resolveItem(ctx, code); ok {
		return line, true
	}
	if line, ok := a.resolveShortcut(ctx, code); ok {
		return line, true
	}
	if line, ok := a.resolveTextSearch(ctx, code); ok {
		return line, true
	}
	return pos.BasketLine{}, false
}

func (a PriceResolverAdapter) resolveVariant(ctx context.Context, code string) (pos.BasketLine, bool) {
	line, ok := a.resolve(ctx, code)
	if ok && line.VariantID != "" {
		return line, true
	}
	return pos.BasketLine{}, false
}

func (a PriceResolverAdapter) resolveItem(ctx context.Context, code string) (pos.BasketLine, bool) {
	line, ok := a.resolve(ctx, code)
	if ok && line.ItemID != "" && line.VariantID == "" {
		return line, true
	}
	return pos.BasketLine{}, false
}

func (a PriceResolverAdapter) resolveShortcut(ctx context.Context, code string) (pos.BasketLine, bool) {
	line, ok := a.resolve(ctx, code)
	if ok && line.ItemID != "" {
		return line, true
	}
	return pos.BasketLine{}, false
}

// resolveTextSearch falls back to SKU or name search when barcode lookups miss.
func (a PriceResolverAdapter) resolveTextSearch(ctx context.Context, code string) (pos.BasketLine, bool) {
	q := strings.TrimSpace(code)
	if q == "" {
		return pos.BasketLine{}, false
	}

	line, ok := a.resolve(ctx, q)
	if ok {
		return line, true
	}
	return pos.BasketLine{}, false
}

func (a PriceResolverAdapter) resolve(ctx context.Context, code string) (pos.BasketLine, bool) {
	row, ok := a.Store.posRepo.ResolveShortcutLine(ctx, code)
	if !ok {
		return pos.BasketLine{}, false
	}
	line := pos.BasketLine{
		SKU:        row.SKU,
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
	return line, true
}
