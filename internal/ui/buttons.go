package ui

import (
	"context"
	"database/sql"
	"errors"
	"html/template"
	"net/http"
	"strings"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/money"
	pos "github.com/universaltill/universal-till/internal/pos"
)

// Button represents a shortcut button backed by the shortcut_buttons table.
type Button struct {
	Label    string `json:"label"`
	Code     string `json:"code"` // barcode/PLU associated with the shortcut
	ItemID   string `json:"itemId"`
	ImageURL string `json:"imageUrl,omitempty"`
	Price    int64  `json:"price,omitempty"` // minor units, display only
}

// ButtonVM is the view-model passed to templates.
type ButtonVM struct {
	Label    string `json:"label"`
	Code     string `json:"code"`
	ItemID   string `json:"itemId"`
	ImageURL string `json:"imageUrl,omitempty"`
	Price    int64  `json:"price,omitempty"` // minor units, display only
}

func ToVM(b []Button) []ButtonVM {
	out := make([]ButtonVM, 0, len(b))
	for _, x := range b {
		out = append(out, ButtonVM{
			Label:    x.Label,
			Code:     x.Code,
			ItemID:   x.ItemID,
			ImageURL: x.ImageURL,
			Price:    x.Price,
		})
	}
	return out
}

// ButtonStore persists shortcut buttons in the shortcut_buttons table via repo.
type ButtonStore struct {
	repo    *data.ShortcutsRepo
	posRepo *data.POSRepo
}

func NewButtonStore(db *sql.DB) *ButtonStore {
	return &ButtonStore{
		repo:    data.NewShortcutsRepo(db),
		posRepo: data.NewPOSRepo(db),
	}
}

type SearchResult struct {
	ItemID  string
	Name    string
	Barcode string
	Image   string
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
	rows, err := s.repo.LoadButtons(context.Background())
	if err != nil {
		return nil, err
	}
	var out []Button
	for _, b := range rows {
		out = append(out, Button{
			Label:    b.Label,
			Code:     b.Barcode,
			ItemID:   b.ItemID,
			ImageURL: b.ImageURL,
			Price:    b.Price,
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

func NewRenderer(layout, page, partial string, funcs template.FuncMap) (*Renderer, error) {
	// Parse templates with provided funcs (includes T for i18n)
	t, err := template.New("base.html").Funcs(funcs).ParseFiles(
		layout,
		page,
		"web/ui/partials/nav.html",
		partial,
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
	_ = h.View.Render(w, "buttons", map[string]any{
		"Buttons": ToVM(btns),
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
		IsWeighed:  row.IsWeighed,
	}
	if row.ImageURL != "" {
		line.ImageURL = row.ImageURL
	}
	return line, true
}

func (s *ButtonStore) currentPrice(ctx context.Context, itemID *string, variantID *string, fallback int64) int64 {
	// Unused after repo move; kept for interface compatibility if needed.
	price, err := s.posRepo.ResolveCurrentPrice(ctx, deref(itemID), deref(variantID))
	if err != nil {
		return fallback
	}
	return price
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
