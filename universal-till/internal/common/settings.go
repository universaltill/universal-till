package common

import (
	"encoding/json"
	"strconv"
	"strings"

	_ "modernc.org/sqlite"
)

type MenuPlugin struct {
	Route string `json:"route"`
	Label string `json:"label"`
	URL   string `json:"url"`
}

type Settings struct {
	Theme            string                `json:"theme"`
	Currency         string                `json:"currency"`
	Country          string                `json:"country"`
	Region           string                `json:"region"`
	TaxInclusive     bool                  `json:"taxInclusive"`
	TaxRatePct       int                   `json:"taxRatePct"`
	InstalledPlugins map[string]bool       `json:"installedPlugins,omitempty"`
	MenuPlugins      map[string]MenuPlugin `json:"menuPlugins,omitempty"`
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
	return map[string]string{
		"theme":            s.Theme,
		"currency":         s.Currency,
		"country":          s.Country,
		"region":           s.Region,
		"taxInclusive":     map[bool]string{true: "true", false: "false"}[s.TaxInclusive],
		"taxRatePct":       strconv.Itoa(s.TaxRatePct),
		"installedPlugins": inst,
		"menuPlugins":      menus,
	}
}
