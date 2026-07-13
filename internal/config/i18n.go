package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// I18n is a tiny JSON-based translator optimized for low-memory devices.
type I18n struct {
	mu       sync.RWMutex
	messages map[string]map[string]string // locale -> key -> message (base files)
	overlays map[string]map[string]string // locale -> key -> message (language-pack plugins)
	shop     map[string]map[string]string // locale -> key -> message (manager edits; win over everything)
	fallback string
}

func NewI18n(localesDir string, fallback string) (*I18n, error) {
	i := &I18n{messages: make(map[string]map[string]string), overlays: make(map[string]map[string]string), shop: make(map[string]map[string]string), fallback: fallback}
	entries, err := os.ReadDir(localesDir)
	if err != nil {
		return nil, err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		locale := strings.TrimSuffix(name, ".json")
		b, err := os.ReadFile(filepath.Join(localesDir, name))
		if err != nil {
			return nil, err
		}
		var m map[string]string
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, fmt.Errorf("i18n %s: %w", name, err)
		}
		i.messages[locale] = m
	}
	if _, ok := i.messages[fallback]; !ok {
		i.messages[fallback] = map[string]string{}
	}
	return i, nil
}

// T returns the translation for key in the given locale, falling back to the
// base language (en-US -> en), then the default locale, then its base. Locale
// files are keyed by base language (en.json, fa.json) while configuration and
// requests may carry full BCP 47 tags.
func (i *I18n) T(locale, key string) string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	for _, loc := range []string{locale, baseLang(locale), i.fallback, baseLang(i.fallback)} {
		// Shop overrides (manager edits) win over everything; then base files,
		// so a plugin cannot hijack core strings; overlays add new locales
		// (language packs) and fill missing keys.
		if m, ok := i.shop[loc]; ok {
			if v, ok := m[key]; ok {
				return v
			}
		}
		if m, ok := i.messages[loc]; ok {
			if v, ok := m[key]; ok {
				return v
			}
		}
		if m, ok := i.overlays[loc]; ok {
			if v, ok := m[key]; ok {
				return v
			}
		}
	}
	return key
}

// SetShopOverrides atomically replaces all manager-edited translations
// (docs repo: architecture/translation-editor.md).
func (i *I18n) SetShopOverrides(shop map[string]map[string]string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if shop == nil {
		shop = map[string]map[string]string{}
	}
	i.shop = shop
}

// TranslationEntry is one row of the translation editor: the key, its
// effective value for a locale, which layer supplied it, and the reference
// (fallback-locale) text shown beside the edit box.
type TranslationEntry struct {
	Key       string
	Value     string
	Source    string // "shop" | "base" | "plugin" | "" (untranslated)
	Reference string
}

// Entries lists every known key (base fallback + locale base + plugin
// overlays + shop overrides) with its effective value for the locale.
func (i *I18n) Entries(locale string) []TranslationEntry {
	i.mu.RLock()
	defer i.mu.RUnlock()
	keys := map[string]bool{}
	for _, loc := range []string{i.fallback, baseLang(i.fallback), locale, baseLang(locale)} {
		for k := range i.messages[loc] {
			keys[k] = true
		}
		for k := range i.overlays[loc] {
			keys[k] = true
		}
		for k := range i.shop[loc] {
			keys[k] = true
		}
	}
	out := make([]TranslationEntry, 0, len(keys))
	for k := range keys {
		e := TranslationEntry{Key: k, Reference: i.lookupLocked(i.fallback, k)}
		e.Value, e.Source = i.lookupWithSourceLocked(locale, k)
		out = append(out, e)
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Key < out[b].Key })
	return out
}

func (i *I18n) lookupLocked(locale, key string) string {
	v, _ := i.lookupWithSourceLocked(locale, key)
	return v
}

// lookupWithSourceLocked mirrors T's precedence for ONE locale (no fallback
// chain — the editor shows what this locale itself defines).
func (i *I18n) lookupWithSourceLocked(locale, key string) (string, string) {
	for _, loc := range []string{locale, baseLang(locale)} {
		if v, ok := i.shop[loc][key]; ok {
			return v, "shop"
		}
		if v, ok := i.messages[loc][key]; ok {
			return v, "base"
		}
		if v, ok := i.overlays[loc][key]; ok {
			return v, "plugin"
		}
	}
	return "", ""
}

// SetOverlays atomically replaces all plugin-provided translations
// (language-pack plugins; resynced on every plugin lifecycle change).
func (i *I18n) SetOverlays(overlays map[string]map[string]string) {
	i.mu.Lock()
	defer i.mu.Unlock()
	if overlays == nil {
		overlays = map[string]map[string]string{}
	}
	i.overlays = overlays
}

// Available returns the sorted locale codes with any translations.
func (i *I18n) Available() []string {
	i.mu.RLock()
	defer i.mu.RUnlock()
	seen := map[string]bool{}
	for loc, m := range i.messages {
		if len(m) > 0 {
			seen[loc] = true
		}
	}
	for loc, m := range i.overlays {
		if len(m) > 0 {
			seen[loc] = true
		}
	}
	out := make([]string, 0, len(seen))
	for loc := range seen {
		out = append(out, loc)
	}
	sort.Strings(out)
	return out
}

// baseLang strips the region from a BCP 47 tag: "en-US" -> "en".
func baseLang(locale string) string {
	if idx := strings.IndexAny(locale, "-_"); idx > 0 {
		return locale[:idx]
	}
	return locale
}
