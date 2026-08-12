package pages

import (
	"os"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/httpx"
)

// Offline system-locale detection for the setup wizard (ut-docs#590, child 1
// of the zero-touch-setup epic ut-docs#589). Deliberately NOT IP geolocation
// — see the card's own rationale: offline-first (ADR-0003) requires a till to
// set itself up with no network, IP lookup fails exactly when a shop's WiFi
// isn't configured yet, and it's often simply wrong (VPNs, tethering,
// business ISPs registered elsewhere). The OS's own locale + timezone are a
// signal we already have for free, fully offline.
//
// osLocaleEnv/osTimezoneName are swappable seams (same pattern as
// catalog/handlers.go's newLookupClient) so tests can simulate a till booted
// under a given system locale/timezone without touching real env vars or
// process-wide time.Local.
var osLocaleEnv = func() string {
	if v := os.Getenv("LC_ALL"); v != "" {
		return v
	}
	return os.Getenv("LANG")
}

var osTimezoneName = func() string { return systemTimezoneName() }

// etcTimezonePath/etcLocaltimePath are vars so the tests can point them at a
// fixture instead of the real machine's configuration.
var (
	etcTimezonePath  = "/etc/timezone"
	etcLocaltimePath = "/etc/localtime"
)

// systemTimezoneName returns this device's IANA zone name ("Europe/Berlin"),
// or "" if it can't be established.
//
// time.Local.String() on its own is NOT enough, and getting this wrong is
// silent: Go only keeps the real zone name when the TZ environment variable
// is set. With TZ unset — the normal case for a systemd-managed till, and for
// most Linux desktops, where the timezone is configured through
// /etc/localtime + /etc/timezone and nothing exports TZ — Go loads the right
// zone but deliberately renames it to the literal "Local"
// (time.initLocal), which matches nothing in setupTimezoneCountry. Without
// the file fallbacks below, timezone-based country detection would therefore
// never fire on a real till, and the wizard would quietly fall back to the
// locale region alone (nothing at all for the common LANG=C.UTF-8 appliance
// image). Both fallbacks are plain local file reads — no network, per
// ADR-0003.
func systemTimezoneName() string {
	if n := time.Local.String(); n != "" && n != "Local" {
		return n
	}
	return timezoneNameFromFiles()
}

// timezoneNameFromFiles reads the zone name the OS itself stores on disk.
// Split out from systemTimezoneName so it is testable against fixtures — the
// time.Local half depends on how the test process itself was started and
// can't be swapped after time's one-time init.
func timezoneNameFromFiles() string {
	// Debian/Ubuntu/Raspberry Pi OS record the name here verbatim.
	if b, err := os.ReadFile(etcTimezonePath); err == nil {
		if n := strings.TrimSpace(string(b)); n != "" {
			return n
		}
	}
	// Everywhere else (Fedora, Arch, Alpine, …) /etc/localtime is a symlink
	// into the zoneinfo tree: /usr/share/zoneinfo/Europe/Berlin.
	if target, err := os.Readlink(etcLocaltimePath); err == nil {
		if _, zone, ok := strings.Cut(target, "/zoneinfo/"); ok && zone != "" {
			return zone
		}
	}
	return ""
}

// setupTimezoneCountry maps common IANA zone names to a setupCountries code.
// Not exhaustive — only the countries the wizard already knows how to
// prefill (setupCountries). An unmapped zone deliberately resolves to "": the
// card's own instruction is "leave it unset rather than guessing."
var setupTimezoneCountry = map[string]string{
	"Europe/London":       "GB",
	"Asia/Tehran":         "IR",
	"Europe/Berlin":       "DE",
	"Europe/Paris":        "FR",
	"Europe/Madrid":       "ES",
	"Europe/Rome":         "IT",
	"Europe/Amsterdam":    "NL",
	"Europe/Istanbul":     "TR",
	"Europe/Ankara":       "TR", // deprecated tzdata alias for Europe/Istanbul
	"Asia/Dubai":          "AE",
	"Asia/Riyadh":         "SA",
	"Asia/Kolkata":        "IN",
	"Asia/Calcutta":       "IN", // pre-1996 tzdata alias for Asia/Kolkata
	"Asia/Karachi":        "PK",
	"America/New_York":    "US",
	"America/Chicago":     "US",
	"America/Denver":      "US",
	"America/Los_Angeles": "US",
	"America/Anchorage":   "US",
	"America/Phoenix":     "US",
	"Pacific/Honolulu":    "US",
}

// parseLocaleEnv splits a POSIX locale value ("de_DE.UTF-8", "en_GB",
// "C.UTF-8", "") into a lowercase language and an uppercase region. "C" and
// "POSIX" (the untranslated default) carry no language signal.
func parseLocaleEnv(v string) (lang, region string) {
	v = strings.TrimSpace(v)
	if v == "" {
		return "", ""
	}
	if i := strings.IndexAny(v, ".@"); i > 0 {
		v = v[:i]
	}
	parts := strings.SplitN(v, "_", 2)
	lang = strings.ToLower(parts[0])
	if lang == "c" || lang == "posix" {
		return "", ""
	}
	if len(parts) > 1 {
		region = strings.ToUpper(parts[1])
	}
	return lang, region
}

// detectCountry returns a setupCountries code from the OS timezone, falling
// back to the locale's own region if the timezone doesn't resolve (e.g. a
// timezone spanning several countries). Returns "" — never a guess — when
// neither signal matches a country this wizard knows about.
func detectCountry() string {
	if c := setupTimezoneCountry[osTimezoneName()]; c != "" {
		return c
	}
	_, region := parseLocaleEnv(osLocaleEnv())
	if region == "" {
		return ""
	}
	for _, c := range setupCountries {
		if c.Code != "OTHER" && c.Code == region {
			return c.Code
		}
	}
	return ""
}

// detectLanguage returns the OS-detected language code and whether it is one
// of the locales this till actually ships (httpx.AvailableLocales() — core
// files plus any installed language-pack plugin overlay). code is "" when
// nothing could be detected at all (e.g. locale env unset, "C"/"POSIX").
func detectLanguage() (code string, available bool) {
	lang, _ := parseLocaleEnv(osLocaleEnv())
	if lang == "" {
		return "", false
	}
	for _, a := range httpx.AvailableLocales() {
		if a == lang {
			return lang, true
		}
	}
	return lang, false
}
