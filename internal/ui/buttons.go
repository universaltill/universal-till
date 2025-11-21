package ui

import (
	"database/sql"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"strings"

	pos "github.com/universaltill/universal-till/internal/pos"
)

type Button struct {
	Label      string `json:"label"`
	Code       string `json:"code"`
	PriceCents int64  `json:"priceCents"`
	ImageURL   string `json:"imageUrl,omitempty"`
}

// ButtonVM is the view-model passed to the template
type ButtonVM struct {
	Label      string `json:"label"`
	Code       string `json:"code"`
	PriceCents int64  `json:"priceCents"`
	Price      string `json:"price"` // Pre-formatted string (e.g. "2.50")
	ImageURL   string `json:"imageUrl,omitempty"`
}

func ToVM(b []Button) []ButtonVM {
	out := make([]ButtonVM, 0, len(b))
	for _, x := range b {
		out = append(out, ButtonVM{
			Label:      x.Label,
			Code:       x.Code,
			PriceCents: x.PriceCents,
			Price:      fmt.Sprintf("%.2f", float64(x.PriceCents)/100.0),
			ImageURL:   x.ImageURL,
		})
	}
	return out
}

// ButtonStore defines persistence for quick buttons.
// type ButtonStore interface {
// 	Load() ([]Button, error)
// 	Save([]Button) error
// 	Add(Button) error
// 	Remove(code string) error
// }

// type FileButtonStore struct {
// 	path string
// 	mu   sync.RWMutex
// }

type ButtonStore struct {
	db *sql.DB
}

// func NewSQLiteButtonStore(db *sql.DB) (*Store, error) {

// 	return &Store{db: db}, nil
// }

// NewButtonStore selects storage by env UT_STORE ("sqlite" to use SQLite). Defaults to file JSON.
func NewButtonStore(db *sql.DB) *ButtonStore {
	return &ButtonStore{db: db}
}

func (s *ButtonStore) Load() ([]Button, error) {
	rows, err := s.db.Query(`SELECT label, code, image_path FROM buttons ORDER BY label`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Button
	for rows.Next() {
		var b Button
		var img sql.NullString
		if err := rows.Scan(&b.Label, &b.Code, &b.PriceCents, &img); err != nil {
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
	if _, err := tx.Exec(`DELETE FROM buttons`); err != nil {
		tx.Rollback()
		return err
	}
	stmt, err := tx.Prepare(`INSERT INTO buttons(code,label,price_cents,image_url) VALUES(?,?,?,?)`)
	if err != nil {
		tx.Rollback()
		return err
	}
	defer stmt.Close()
	for _, b := range list {
		if _, err := stmt.Exec(b.Code, b.Label, b.PriceCents, nullIfEmpty(b.ImageURL)); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *ButtonStore) Add(btn Button) error {
	btn.Label = strings.TrimSpace(btn.Label)
	btn.Code = strings.TrimSpace(btn.Code)
	if btn.Label == "" || btn.Code == "" {
		return errors.New("label and code are required")
	}
	_, err := s.db.Exec(`INSERT INTO buttons(code,label,price_cents,image_url) VALUES(?,?,?,?)
	ON CONFLICT(code) DO UPDATE SET label=excluded.label, price_cents=excluded.price_cents, image_url=excluded.image_url`,
		btn.Code, btn.Label, btn.PriceCents, nullIfEmpty(btn.ImageURL))
	return err
}

func (s *ButtonStore) Remove(code string) error {
	_, err := s.db.Exec(`DELETE FROM buttons WHERE code=?`, strings.TrimSpace(code))
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
	price := int64(0)
	fmt.Sscan(r.Form.Get("priceCents"), &price)
	img := strings.TrimSpace(r.Form.Get("imageUrl"))
	if img != "" && !strings.HasPrefix(img, "http://") && !strings.HasPrefix(img, "https://") && !strings.HasPrefix(img, "/public/") {
		// Treat as filename in local images folder
		img = "/public/images/" + img
	}
	err := h.Store.Add(Button{
		Label:      r.Form.Get("label"),
		Code:       r.Form.Get("code"),
		PriceCents: price,
		ImageURL:   img,
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
	list, err := a.Store.Load()
	if err != nil {
		return pos.BasketLine{}, false
	}
	for _, b := range list {
		if strings.EqualFold(b.Code, code) {
			return pos.BasketLine{SKU: b.Code, Name: b.Label, Qty: 1, PriceCents: b.PriceCents, ImageURL: b.ImageURL}, true
		}
	}
	return pos.BasketLine{}, false
}

func nullIfEmpty(s string) any {
	if strings.TrimSpace(s) == "" {
		return nil
	}
	return s
}
