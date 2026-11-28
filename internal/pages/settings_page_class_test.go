package pages

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/registrydesc"
	"github.com/universaltill/universal-till/internal/uislot"
)

// ADR-0190 / ut-docs#6332 S3: a class is only useful if it can attach to every
// card. Every .card id in the real settings.html must have a CoreSettings row
// with a valid class.
func TestSettingsHTMLCardsAllHaveCoreRow(t *testing.T) {
	chdirRoot(t)
	raw, err := os.ReadFile(filepath.Join("web", "ui", "pages", "settings.html"))
	if err != nil {
		t.Fatalf("read settings.html: %v", err)
	}
	cardRE := regexp.MustCompile(`<div class="card[^"]*" id="([^"]+)"`)
	n := 0
	for _, m := range cardRE.FindAllStringSubmatch(string(raw), -1) {
		if len(m[1]) >= 2 && m[1][:2] == "{{" { // plugin section card, listed per render
			continue
		}
		n++
		row, ok := uislot.CoreSettingsEntry(m[1])
		if !ok {
			t.Errorf("settings.html card %q has no CoreSettings row, so no class can attach", m[1])
			continue
		}
		if !row.Class.Valid() {
			t.Errorf("card %q row has invalid class %q", m[1], row.Class)
		}
	}
	if n == 0 {
		t.Fatal("found no cards in settings.html: the regex or the file drifted")
	}
}

// Every registry region names a real card (or a documented sub-region whose
// containing card exists), so the registry can echo the row's class (S1').
func TestRegistryRegionsNameARealCard(t *testing.T) {
	for _, s := range data.AgentSettings {
		if s.Region == "" {
			continue // a setting that lives on its own page (Page is set)
		}
		if _, ok := registrydesc.ClassForRegion(s.Region); !ok {
			t.Errorf("registry key %q region %q names no CoreSettings card (add the row, or map the sub-region in uislot regionCard)", s.Key, s.Region)
		}
	}
}
