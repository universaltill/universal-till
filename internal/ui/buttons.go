package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

type Button struct {
	Label      string `json:"label"`
	Code       string `json:"code"`
	PriceCents int64  `json:"priceCents"`
}

// ButtonVM is the view-model passed to the template
type ButtonVM struct {
	Label      string `json:"label"`
	Code       string `json:"code"`
	PriceCents int64  `json:"priceCents"`
	Price      string `json:"price"` // Pre-formatted string (e.g. "2.50")
}

func toVM(b []Button) []ButtonVM {
	out := make([]ButtonVM, 0, len(b))
	for _, x := range b {
		out = append(out, ButtonVM{
			Label:      x.Label,
			Code:       x.Code,
			PriceCents: x.PriceCents,
			Price:      fmt.Sprintf("%.2f", float64(x.PriceCents)/100.0),
		})
	}
	return out
}

type ButtonStore struct {
	path string
	mu   sync.RWMutex
}

func NewButtonStore(dataDir string) *ButtonStore {
	return &ButtonStore{path: filepath.Join(dataDir, "buttons.json")}
}

func (s *ButtonStore) Load() ([]Button, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	b, err := os.ReadFile(s.path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []Button{}, nil
		}
		return nil, err
	}
	var out []Button
	if err := json.Unmarshal(b, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (s *ButtonStore) Save(list []Button) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	b, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func (s *ButtonStore) Add(btn Button) error {
	btn.Label = strings.TrimSpace(btn.Label)
	btn.Code = strings.TrimSpace(btn.Code)
	if btn.Label == "" || btn.Code == "" {
		return fmt.Errorf("label and code are required")
	}

	list, err := s.Load()
	if err != nil {
		return err
	}
	// replace if same code exists
	replaced := false
	for i := range list {
		if strings.EqualFold(list[i].Code, btn.Code) {
			list[i] = btn
			replaced = true
			break
		}
	}
	if !replaced {
		list = append(list, btn)
	}
	return s.Save(list)
}

func (s *ButtonStore) Remove(code string) error {
	list, err := s.Load()
	if err != nil {
		return err
	}
	code = strings.TrimSpace(code)
	out := make([]Button, 0, len(list))
	for _, b := range list {
		if !strings.EqualFold(b.Code, code) {
			out = append(out, b)
		}
	}
	return s.Save(out)
}

/* ----------------- HTTP handlers (htmx-friendly) ----------------- */

type TplRenderer interface {
	Render(w http.ResponseWriter, name string, data any) error
}

type Renderer struct {
	t *template.Template
}

func NewRenderer(layout, page, partial string) (*Renderer, error) {
	// Just parse templates; no FuncMap required since we pre-format data.
	t, err := template.ParseFiles(
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
	Store *ButtonStore
	View  TplRenderer
}

func (h *ButtonsHTTP) List(w http.ResponseWriter, r *http.Request) {
	btns, _ := h.Store.Load()
	_ = h.View.Render(w, "buttons", map[string]any{
		"Buttons": toVM(btns),
	})
}

func (h *ButtonsHTTP) Add(w http.ResponseWriter, r *http.Request) {
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	price := int64(0)
	fmt.Sscan(r.Form.Get("priceCents"), &price)
	err := h.Store.Add(Button{
		Label:      r.Form.Get("label"),
		Code:       r.Form.Get("code"),
		PriceCents: price,
	})
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	h.List(w, r) // Re-render with formatted prices
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
	h.List(w, r)
}
