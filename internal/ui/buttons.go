package ui

import (
	"context"
	"database/sql"
	"errors"
	"html/template"
	"net/http"
	"strings"

	pos "github.com/universaltill/universal-till/internal/pos"
)

// Button represents a shortcut button backed by the shortcut_buttons table.
type Button struct {
	Label    string `json:"label"`
	Code     string `json:"code"` // barcode/PLU associated with the shortcut
	ItemID   string `json:"itemId"`
	ImageURL string `json:"imageUrl,omitempty"`
}

// ButtonVM is the view-model passed to templates.
type ButtonVM struct {
	Label    string `json:"label"`
	Code     string `json:"code"`
	ItemID   string `json:"itemId"`
	ImageURL string `json:"imageUrl,omitempty"`
}

func ToVM(b []Button) []ButtonVM {
	out := make([]ButtonVM, 0, len(b))
	for _, x := range b {
		out = append(out, ButtonVM{
			Label:    x.Label,
			Code:     x.Code,
			ItemID:   x.ItemID,
			ImageURL: x.ImageURL,
		})
	}
	return out
}

// ButtonStore persists shortcut buttons in the shortcut_buttons table.
type ButtonStore struct {
	db *sql.DB
}

func NewButtonStore(db *sql.DB) *ButtonStore {
	return &ButtonStore{db: db}
}

type SearchResult struct {
	ItemID  string
	Name    string
	Barcode string
	Image   string
}

// SearchItems finds items (and primary barcodes) to add as shortcuts.
func (s *ButtonStore) SearchItems(ctx context.Context, q string, offset, limit int) ([]SearchResult, error) {
	like := "%" + strings.TrimSpace(q) + "%"
	if limit <= 0 {
		limit = 10
	}
	if offset < 0 {
		offset = 0
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT i.id,
       i.name,
       (
         SELECT ib.barcode
         FROM item_barcodes ib
         WHERE ib.item_id = i.id
         ORDER BY ib.is_primary DESC
         LIMIT 1
       ) AS barcode,
       COALESCE(img.path, '')
FROM items i
LEFT JOIN item_images img ON img.item_id = i.id AND img.role = 'thumbnail'
WHERE i.is_active = 1 AND (
	  i.name LIKE ?
	  OR i.sku LIKE ?
	  OR EXISTS (SELECT 1 FROM item_barcodes ib2 WHERE ib2.item_id = i.id AND ib2.barcode LIKE ?)
)
ORDER BY i.name
LIMIT ? OFFSET ?
`, like, like, like, limit, offset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []SearchResult
	for rows.Next() {
		var r SearchResult
		if err := rows.Scan(&r.ItemID, &r.Name, &r.Barcode, &r.Image); err != nil {
			return nil, err
		}
		res = append(res, r)
	}
	return res, rows.Err()
}

func (s *ButtonStore) Load() ([]Button, error) {
	rows, err := s.db.Query(`SELECT label, barcode, item_id, image_path FROM shortcut_buttons ORDER BY label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Button
	for rows.Next() {
		var b Button
		var img sql.NullString
		if err := rows.Scan(&b.Label, &b.Code, &b.ItemID, &img); err != nil {
			return nil, err
		}
		if img.Valid {
			b.ImageURL = img.String
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (s *ButtonStore) Save(list []Button) error {
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM shortcut_buttons`); err != nil {
		tx.Rollback()
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO shortcut_buttons(barcode,label,item_id,image_path) VALUES(?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, b := range list {
		if _, err := stmt.Exec(b.Code, b.Label, b.ItemID, nullIfEmpty(b.ImageURL)); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *ButtonStore) Add(btn Button) error {
	btn.Label = strings.TrimSpace(btn.Label)
	btn.Code = strings.TrimSpace(btn.Code)
	btn.ItemID = strings.TrimSpace(btn.ItemID)
	if btn.Label == "" || btn.Code == "" || btn.ItemID == "" {
		return errors.New("label, code, and itemId are required")
	}
	_, err := s.db.Exec(`INSERT INTO shortcut_buttons(barcode,label,item_id,image_path) VALUES(?,?,?,?)
	ON CONFLICT(barcode) DO UPDATE SET label=excluded.label, item_id=excluded.item_id, image_path=excluded.image_path`,
		btn.Code, btn.Label, btn.ItemID, nullIfEmpty(btn.ImageURL))
	return err
}

func (s *ButtonStore) Remove(code string) error {
	_, err := s.db.Exec(`DELETE FROM shortcut_buttons WHERE barcode=?`, strings.TrimSpace(code))
	return err
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
	row := a.Store.db.QueryRowContext(ctx, `
SELECT i.id, i.name, v.id, v.name, v.price,
       (SELECT path FROM item_images img WHERE img.item_id = i.id AND img.role = 'thumbnail' LIMIT 1),
       COALESCE(t.rate_basis_points, 0)
FROM variant_barcodes vb
JOIN item_variants v ON v.id = vb.variant_id
JOIN items i ON i.id = v.item_id
LEFT JOIN tax_codes t ON t.id = i.tax_code_id
WHERE vb.barcode = ?
  AND i.is_active = 1 AND v.is_active = 1
LIMIT 1
`, code)
	var itemID, itemName, variantID, variantName, img sql.NullString
	var price int64
	var rateBP sql.NullInt64
	if err := row.Scan(&itemID, &itemName, &variantID, &variantName, &price, &img, &rateBP); err != nil {
		return pos.BasketLine{}, false
	}
	price = a.Store.currentPrice(ctx, nil, &variantID.String, price)
	name := itemName.String
	if variantName.String != "" {
		name = name + " - " + variantName.String
	}
	line := pos.BasketLine{SKU: code, Name: name, Qty: 1, PriceCents: price, ItemID: itemID.String, VariantID: variantID.String, TaxRateBP: int(rateBP.Int64)}
	if img.Valid {
		line.ImageURL = img.String
	}
	return line, true
}

func (a PriceResolverAdapter) resolveItem(ctx context.Context, code string) (pos.BasketLine, bool) {
	row := a.Store.db.QueryRowContext(ctx, `
SELECT i.id, i.name, i.base_price,
       (SELECT path FROM item_images img WHERE img.item_id = i.id AND img.role = 'thumbnail' LIMIT 1),
       COALESCE(t.rate_basis_points, 0)
FROM item_barcodes ib
JOIN items i ON i.id = ib.item_id
LEFT JOIN tax_codes t ON t.id = i.tax_code_id
WHERE ib.barcode = ?
  AND i.is_active = 1
LIMIT 1
`, code)
	var itemID, name, img sql.NullString
	var price int64
	var rateBP sql.NullInt64
	if err := row.Scan(&itemID, &name, &price, &img, &rateBP); err != nil {
		return pos.BasketLine{}, false
	}
	price = a.Store.currentPrice(ctx, &itemID.String, nil, price)
	line := pos.BasketLine{SKU: code, Name: name.String, Qty: 1, PriceCents: price, ItemID: itemID.String, TaxRateBP: int(rateBP.Int64)}
	if img.Valid {
		line.ImageURL = img.String
	}
	return line, true
}

func (a PriceResolverAdapter) resolveShortcut(ctx context.Context, code string) (pos.BasketLine, bool) {
	row := a.Store.db.QueryRowContext(ctx, `
SELECT sb.item_id, sb.label, i.base_price,
       (SELECT path FROM item_images img WHERE img.item_id = i.id AND img.role = 'thumbnail' LIMIT 1),
       COALESCE(t.rate_basis_points, 0)
FROM shortcut_buttons sb
JOIN items i ON i.id = sb.item_id
LEFT JOIN tax_codes t ON t.id = i.tax_code_id
WHERE sb.barcode = ?
  AND i.is_active = 1
LIMIT 1
`, code)
	var itemID, label, img sql.NullString
	var price int64
	var rateBP sql.NullInt64
	if err := row.Scan(&itemID, &label, &price, &img, &rateBP); err != nil {
		return pos.BasketLine{}, false
	}
	price = a.Store.currentPrice(ctx, &itemID.String, nil, price)
	line := pos.BasketLine{SKU: code, Name: label.String, Qty: 1, PriceCents: price, ItemID: itemID.String, TaxRateBP: int(rateBP.Int64)}
	if img.Valid {
		line.ImageURL = img.String
	}
	return line, true
}

// resolveTextSearch falls back to SKU or name search when barcode lookups miss.
func (a PriceResolverAdapter) resolveTextSearch(ctx context.Context, code string) (pos.BasketLine, bool) {
	q := strings.TrimSpace(code)
	if q == "" {
		return pos.BasketLine{}, false
	}

	// Try exact SKU match first
	row := a.Store.db.QueryRowContext(ctx, `
SELECT i.id, i.sku, i.name, i.base_price,
       (SELECT path FROM item_images img WHERE img.item_id = i.id AND img.role = 'thumbnail' LIMIT 1),
       COALESCE(t.rate_basis_points, 0)
FROM items i
LEFT JOIN tax_codes t ON t.id = i.tax_code_id
WHERE i.is_active = 1 AND i.sku = ?
LIMIT 1
`, q)
	var itemID, sku, name, img sql.NullString
	var price int64
	var rateBP sql.NullInt64
	if err := row.Scan(&itemID, &sku, &name, &price, &img, &rateBP); err == nil {
		price = a.Store.currentPrice(ctx, &itemID.String, nil, price)
		line := pos.BasketLine{SKU: sku.String, Name: name.String, Qty: 1, PriceCents: price, ItemID: itemID.String, TaxRateBP: int(rateBP.Int64)}
		if img.Valid {
			line.ImageURL = img.String
		}
		return line, true
	}

	// Partial name match
	like := "%" + q + "%"
	row = a.Store.db.QueryRowContext(ctx, `
SELECT i.id, i.name, i.base_price,
       (SELECT path FROM item_images img WHERE img.item_id = i.id AND img.role = 'thumbnail' LIMIT 1),
       COALESCE(t.rate_basis_points, 0)
FROM items i
LEFT JOIN tax_codes t ON t.id = i.tax_code_id
WHERE i.is_active = 1 AND i.name LIKE ?
ORDER BY i.name
LIMIT 1
`, like)
	var name2, img2 sql.NullString
	var price2 int64
	var rateBP2 sql.NullInt64
	if err := row.Scan(&itemID, &name2, &price2, &img2, &rateBP2); err != nil {
		return pos.BasketLine{}, false
	}
	price2 = a.Store.currentPrice(ctx, &itemID.String, nil, price2)
	line := pos.BasketLine{SKU: q, Name: name2.String, Qty: 1, PriceCents: price2, ItemID: itemID.String, TaxRateBP: int(rateBP2.Int64)}
	if img2.Valid {
		line.ImageURL = img2.String
	}
	return line, true
}

func (s *ButtonStore) currentPrice(ctx context.Context, itemID *string, variantID *string, fallback int64) int64 {
	price, err := pos.ResolveCurrentPrice(ctx, s.db, deref(itemID), deref(variantID))
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
