package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// I18n is a tiny JSON-based translator optimized for low-memory devices.
type I18n struct {
	mu       sync.RWMutex
	messages map[string]map[string]string // locale -> key -> message
	fallback string
}

func NewI18n(localesDir string, fallback string) (*I18n, error) {
	i := &I18n{messages: make(map[string]map[string]string), fallback: fallback}
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
		if m, ok := i.messages[loc]; ok {
			if v, ok := m[key]; ok {
				return v
			}
		}
	}
	return key
}

// baseLang strips the region from a BCP 47 tag: "en-US" -> "en".
func baseLang(locale string) string {
	if idx := strings.IndexAny(locale, "-_"); idx > 0 {
		return locale[:idx]
	}
	return locale
}
