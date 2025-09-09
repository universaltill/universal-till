package main

import (
	"encoding/json"
	"log"
	"net/http"
	"os"
)

type Plugin struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Author      string `json:"author"`
	PriceCents  int64  `json:"priceCents"`
	Billing     string `json:"billing"` // one_time | subscription
}

type Store struct {
	plugins []Plugin
}

func (s *Store) list(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.plugins)
}

func (s *Store) submit(w http.ResponseWriter, r *http.Request) {
	var p Plugin
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if p.ID == "" || p.Name == "" {
		http.Error(w, "id and name required", http.StatusBadRequest)
		return
	}
	s.plugins = append(s.plugins, p)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

func (s *Store) purchase(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"status": "ok"})
}

func main() {
	addr := os.Getenv("UT_STORE_ADDR")
	if addr == "" {
		addr = ":9090"
	}
	s := &Store{plugins: []Plugin{}}
	http.HandleFunc("/api/plugins", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			s.submit(w, r)
			return
		}
		s.list(w, r)
	})
	http.HandleFunc("/api/purchase", s.purchase)
	log.Printf("Store listening on %s", addr)
	log.Fatal(http.ListenAndServe(addr, nil))
}
