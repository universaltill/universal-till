package common

import (
	"database/sql"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

type MenuPlugin struct {
	Route string `json:"route"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

type PluginRecord struct {
	Route string `json:"route"`
	Label string `json:"label"`
	Path  string `json:"path"`
}

type Settings struct {
	Theme            string                  `json:"theme"`
	Currency         string                  `json:"currency"`
	Country          string                  `json:"country"`
	Region           string                  `json:"region"`
	TaxInclusive     bool                    `json:"taxInclusive"`
	TaxRatePct       int                     `json:"taxRatePct"`
	InstalledPlugins map[string]bool         `json:"installedPlugins,omitempty"`
	MenuPlugins      map[string]MenuPlugin   `json:"menuPlugins,omitempty"`
	PluginRecords    map[string]PluginRecord `json:"pluginRecords,omitempty"`
}

type SettingsStore interface {
	GetTheme() string
	SetTheme(theme string) error
	GetAll() Settings
	SetAll(s Settings) error
}

type sqliteSettings struct{ db *sql.DB }

type fileSettings struct{ path string }

func NewSettingsStore(dataDir string, preferSQLite bool) SettingsStore {
	if preferSQLite {
		db, err := sql.Open("sqlite", filepath.Join(dataDir, "unitill.db"))
		if err == nil {
			_ = initSettingsSchema(db)
			return &sqliteSettings{db: db}
		}
	}
	return &fileSettings{path: filepath.Join(dataDir, "settings.json")}
}

func initSettingsSchema(db *sql.DB) error {
	_, err := db.Exec(`CREATE TABLE IF NOT EXISTS settings(
	  key TEXT PRIMARY KEY,
	  value TEXT
	);`)
	return err
}

func (s *sqliteSettings) GetTheme() string {
	row := s.db.QueryRow(`SELECT value FROM settings WHERE key='theme'`)
	var v string
	if err := row.Scan(&v); err == nil {
		return v
	}
	return "default"
}

func (s *sqliteSettings) SetTheme(theme string) error {
	_, err := s.db.Exec(`INSERT INTO settings(key,value) VALUES('theme',?)
	  ON CONFLICT(key) DO UPDATE SET value=excluded.value`, strings.TrimSpace(theme))
	return err
}

func (s *sqliteSettings) GetAll() Settings {
	rows, err := s.db.Query(`SELECT key, value FROM settings`)
	if err != nil {
		return Settings{}
	}
	defer rows.Close()
	m := map[string]string{}
	for rows.Next() {
		var k, v string
		_ = rows.Scan(&k, &v)
		m[k] = v
	}
	return mapToSettings(m)
}

func (s *sqliteSettings) SetAll(in Settings) error {
	m := settingsToMap(in)
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	for k, v := range m {
		if _, err := tx.Exec(`INSERT INTO settings(key,value) VALUES(?,?)
		  ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, v); err != nil {
			tx.Rollback()
			return err
		}
	}
	return tx.Commit()
}

func (s *fileSettings) GetTheme() string { return s.GetAll().Theme }

func (s *fileSettings) SetTheme(theme string) error {
	cur := s.GetAll()
	cur.Theme = strings.TrimSpace(theme)
	return s.SetAll(cur)
}

func (s *fileSettings) GetAll() Settings {
	b, err := os.ReadFile(s.path)
	if err != nil {
		return Settings{Theme: "default", Currency: "GBP", Country: "GB", Region: "", TaxRatePct: 20}
	}
	var out Settings
	if json.Unmarshal(b, &out) == nil {
		return out
	}
	return Settings{Theme: "default", Currency: "GBP", Country: "GB", Region: "", TaxRatePct: 20}
}

func (s *fileSettings) SetAll(in Settings) error {
	b, err := json.MarshalIndent(in, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, s.path)
}

func mapToSettings(m map[string]string) Settings {
	out := Settings{Theme: "default", Currency: "GBP", Country: "GB", Region: "", TaxRatePct: 20}
	if v := m["theme"]; v != "" {
		out.Theme = v
	}
	if v := m["currency"]; v != "" {
		out.Currency = v
	}
	if v := m["country"]; v != "" {
		out.Country = v
	}
	if v := m["region"]; v != "" {
		out.Region = v
	}
	if v := m["taxInclusive"]; strings.ToLower(v) == "true" {
		out.TaxInclusive = true
	}
	if v := m["taxRatePct"]; v != "" {
		if n, _ := strconv.Atoi(v); n >= 0 {
			out.TaxRatePct = n
		}
	}
	if v := m["installedPlugins"]; v != "" {
		var mp map[string]bool
		if json.Unmarshal([]byte(v), &mp) == nil {
			out.InstalledPlugins = mp
		}
	}
	if v := m["menuPlugins"]; v != "" {
		var mp map[string]MenuPlugin
		if json.Unmarshal([]byte(v), &mp) == nil {
			out.MenuPlugins = mp
		}
	}
	if v := m["pluginRecords"]; v != "" {
		var mp map[string]PluginRecord
		if json.Unmarshal([]byte(v), &mp) == nil {
			out.PluginRecords = mp
		}
	}
	return out
}

func settingsToMap(s Settings) map[string]string {
	inst := ""
	if s.InstalledPlugins != nil {
		if b, err := json.Marshal(s.InstalledPlugins); err == nil {
			inst = string(b)
		}
	}
	menus := ""
	if s.MenuPlugins != nil {
		if b, err := json.Marshal(s.MenuPlugins); err == nil {
			menus = string(b)
		}
	}
	recs := ""
	if s.PluginRecords != nil {
		if b, err := json.Marshal(s.PluginRecords); err == nil {
			recs = string(b)
		}
	}
	return map[string]string{
		"theme":            s.Theme,
		"currency":         s.Currency,
		"country":          s.Country,
		"region":           s.Region,
		"taxInclusive":     map[bool]string{true: "true", false: "false"}[s.TaxInclusive],
		"taxRatePct":       strconv.Itoa(s.TaxRatePct),
		"installedPlugins": inst,
		"menuPlugins":      menus,
		"pluginRecords":    recs,
	}
}
