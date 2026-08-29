// Standalone locale loading for unitill-uninstall. This CLI runs with no
// HTTP server (so no httpx.T): it reads the same web/locales/<lang>.json
// files the running till serves — installed at /opt/unitill/web/locales —
// directly from disk.
//
// Fallback policy (the documented choice, tested in locale_test.go):
// PER-KEY English fallback. en.json is always loaded as the base; the
// requested locale, when present and parseable, overlays it key by key. A
// missing file, a corrupt file, or a single missing key all degrade to the
// English text for exactly the affected keys — the uninstaller never fails,
// and never prints an empty prompt, over a translation.
package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

type translator struct {
	base map[string]string // en.json
	lang map[string]string // requested locale overlay (may be empty)
}

// loadTranslator never fails: worst case both maps are empty and T returns
// its key — still a greppable, actionable message.
func loadTranslator(localeDir, lang string) *translator {
	tr := &translator{
		base: readLocaleFile(filepath.Join(localeDir, "en.json")),
	}
	// A --lang value is external input: only a plain lowercase code may
	// reach the filesystem (never a path fragment like "../evil").
	lang = strings.ToLower(strings.TrimSpace(lang))
	if lang != "" && lang != "en" && isPlainLangCode(lang) {
		tr.lang = readLocaleFile(filepath.Join(localeDir, lang+".json"))
	}
	return tr
}

func readLocaleFile(path string) map[string]string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}
	m := map[string]string{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]string{}
	}
	return m
}

func isPlainLangCode(lang string) bool {
	for _, r := range lang {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return len(lang) > 0 && len(lang) <= 8
}

func (t *translator) T(key string) string {
	if v, ok := t.lang[key]; ok && v != "" {
		return v
	}
	if v, ok := t.base[key]; ok && v != "" {
		return v
	}
	return key
}

// primaryLang reduces a configured BCP-47 tag (en-US, tr_TR) to the base
// language code the locale files are named by; empty input means English.
func primaryLang(locale string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(locale, "-_"); i >= 0 {
		locale = locale[:i]
	}
	if locale == "" {
		return "en"
	}
	return locale
}

// allUninstallKeys is every locale key this CLI prints. Locale parity
// across ar/fa/tr is guard-i18n.sh's job; tying the keys the CODE uses to
// en.json is this list's job (locale_test.go's TestShippedLocalesCarryAllUsedKeys)
// — the CLI has no template, so the template-scan half of the guard never
// sees these. Keep this list in sync with every T() call site.
var allUninstallKeys = []string{
	"uninstall.title",
	"uninstall.err_root",
	"uninstall.err_no_apt",
	"uninstall.prompt_backup",
	"uninstall.backup_skipped",
	"uninstall.err_unsafe_backup_dest",
	"uninstall.stopping_service",
	"uninstall.backup_creating",
	"uninstall.backup_saved",
	"uninstall.err_backup",
	"uninstall.err_verify",
	"uninstall.abort_nothing_removed",
	"uninstall.prompt_data",
	"uninstall.data_kept",
	"uninstall.data_mismatch",
	"uninstall.data_purge",
	"uninstall.removing",
	"uninstall.err_apt",
	"uninstall.leftover_none",
	"uninstall.leftover_found",
	"uninstall.done",
}
