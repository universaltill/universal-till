package pages

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/httpx"
)

// detectCountryTestCodes mirrors what the wizard actually passes detectCountry
// in production (ut-docs#660): the real country_settings codes, "OTHER"
// excluded (detectCountry's caller is responsible for that, same as it always
// was when detectCountry read setupCountries directly).
func detectCountryTestCodes() []string {
	defaults := data.BuiltinCountryDefaults()
	codes := make([]string, 0, len(defaults))
	for _, d := range defaults {
		if d.Code != "OTHER" {
			codes = append(codes, d.Code)
		}
	}
	return codes
}

// withOSLocale swaps the detection seams for the duration of the test —
// same pattern as catalog/handlers.go's newLookupClient — so no test ever
// touches the real process environment or time.Local.
func withOSLocale(t *testing.T, locale, timezone string) {
	t.Helper()
	origLocale, origTZ := osLocaleEnv, osTimezoneName
	osLocaleEnv = func() string { return locale }
	osTimezoneName = func() string { return timezone }
	t.Cleanup(func() { osLocaleEnv, osTimezoneName = origLocale, origTZ })
}

// Review finding: with TZ unset — the normal case for a systemd-managed till
// — time.Local.String() is the literal "Local", which matches no entry in
// setupTimezoneCountry, so the timezone half of detection would silently
// never fire in the field. These pin the on-disk fallbacks that make it work.
func TestTimezoneNameFromFiles(t *testing.T) {
	dir := t.TempDir()

	// 1. /etc/timezone (Debian / Raspberry Pi OS) wins when present.
	tzFile := filepath.Join(dir, "timezone")
	if err := os.WriteFile(tzFile, []byte("Europe/Berlin\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	origTZFile, origLocaltime := etcTimezonePath, etcLocaltimePath
	t.Cleanup(func() { etcTimezonePath, etcLocaltimePath = origTZFile, origLocaltime })
	etcTimezonePath = tzFile
	etcLocaltimePath = filepath.Join(dir, "localtime")
	if got := timezoneNameFromFiles(); got != "Europe/Berlin" {
		t.Errorf("timezoneNameFromFiles() with /etc/timezone = %q, want %q", got, "Europe/Berlin")
	}

	// 2. Falling back to the /etc/localtime symlink (Fedora/Arch/Alpine).
	etcTimezonePath = filepath.Join(dir, "does-not-exist")
	if err := os.Symlink("/usr/share/zoneinfo/Europe/Istanbul", etcLocaltimePath); err != nil {
		t.Fatal(err)
	}
	if got := timezoneNameFromFiles(); got != "Europe/Istanbul" {
		t.Errorf("timezoneNameFromFiles() via symlink = %q, want %q", got, "Europe/Istanbul")
	}

	// 3. Neither available (minimal image, or /etc/localtime is a plain copy)
	//    → "", so detection falls through to the locale region rather than
	//    guessing.
	etcLocaltimePath = filepath.Join(dir, "no-such-localtime")
	if got := timezoneNameFromFiles(); got != "" {
		t.Errorf("timezoneNameFromFiles() with neither source = %q, want \"\"", got)
	}
}

// And the composition: systemTimezoneName must actually reach those on-disk
// sources when Go hands it the "Local" placeholder, and must never hand that
// placeholder (or an empty string, on a machine that does have a timezone
// configured) out to detectCountry. Written to be deterministic whether or
// not the test process itself was started with TZ exported.
func TestSystemTimezoneNameFallsBackToDiskWhenTZUnset(t *testing.T) {
	dir := t.TempDir()
	tzFile := filepath.Join(dir, "timezone")
	if err := os.WriteFile(tzFile, []byte("Europe/Istanbul\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	origTZFile, origLocaltime := etcTimezonePath, etcLocaltimePath
	t.Cleanup(func() { etcTimezonePath, etcLocaltimePath = origTZFile, origLocaltime })
	etcTimezonePath = tzFile
	etcLocaltimePath = filepath.Join(dir, "no-such-localtime")

	got := systemTimezoneName()
	if got == "Local" || got == "" {
		t.Fatalf("systemTimezoneName() = %q — an unresolved name matches no country and makes the timezone signal a silent no-op", got)
	}
	if local := time.Local.String(); local == "Local" {
		// TZ not exported — the field-realistic case for a systemd-managed
		// till. The on-disk name is the only real signal and must win.
		if got != "Europe/Istanbul" {
			t.Errorf("systemTimezoneName() = %q with TZ unset, want the on-disk %q", got, "Europe/Istanbul")
		}
	} else if got != local {
		// TZ exported — an explicit operator choice, which still wins.
		t.Errorf("systemTimezoneName() = %q, want the explicitly-set %q", got, local)
	}
}

func TestParseLocaleEnv(t *testing.T) {
	cases := []struct {
		in           string
		lang, region string
	}{
		{"de_DE.UTF-8", "de", "DE"},
		{"tr_TR", "tr", "TR"},
		{"en_GB.UTF-8", "en", "GB"},
		{"ja_JP.UTF-8", "ja", "JP"},
		{"fa_IR.UTF-8@euro", "fa", "IR"},
		{"C", "", ""},
		{"C.UTF-8", "", ""},
		{"POSIX", "", ""},
		{"", "", ""},
		{"en", "en", ""},
	}
	for _, tc := range cases {
		lang, region := parseLocaleEnv(tc.in)
		if lang != tc.lang || region != tc.region {
			t.Errorf("parseLocaleEnv(%q) = (%q, %q), want (%q, %q)", tc.in, lang, region, tc.lang, tc.region)
		}
	}
}

func TestDetectCountry(t *testing.T) {
	cases := []struct {
		name, locale, timezone, want string
	}{
		{"Germany by timezone", "de_DE.UTF-8", "Europe/Berlin", "DE"},
		{"Turkey by timezone", "tr_TR.UTF-8", "Europe/Istanbul", "TR"},
		{"UK by timezone", "en_GB.UTF-8", "Europe/London", "GB"},
		{"unmapped timezone falls back to locale region", "de_AT.UTF-8", "Europe/Vienna", ""},
		{"locale region backs a country when timezone is ambiguous", "en_US.UTF-8", "America/New_York", "US"},
		{"nothing detectable at all", "ja_JP.UTF-8", "Asia/Tokyo", ""},
		{"empty everything", "", "", ""},
	}
	codes := detectCountryTestCodes()
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withOSLocale(t, tc.locale, tc.timezone)
			if got := detectCountry(codes); got != tc.want {
				t.Errorf("detectCountry(codes) = %q, want %q", got, tc.want)
			}
		})
	}
}

// detectLanguage depends on httpx's wired translator (AvailableLocales) —
// load the real locale bundle so "which locales are available" matches
// production (core ships ar/en/fa/tr; a plugin like ut-plugin-language-de is
// never installed in this unit test, so "de" is a real, current example of
// "detected but unavailable").
func initDetectTestI18n(t *testing.T) {
	t.Helper()
	chdirRoot(t)
	i18n, err := config.NewI18n(filepath.Join("web", "locales"), "en")
	if err != nil {
		t.Fatalf("i18n: %v", err)
	}
	httpx.InitI18n(i18n, "en")
}

func TestDetectLanguage(t *testing.T) {
	initDetectTestI18n(t)

	cases := []struct {
		name, locale  string
		wantCode      string
		wantAvailable bool
	}{
		{"Turkish is a core locale", "tr_TR.UTF-8", "tr", true},
		{"English is a core locale", "en_GB.UTF-8", "en", true},
		{"German detected but not shipped in core", "de_DE.UTF-8", "de", false},
		{"unsupported locale entirely", "ja_JP.UTF-8", "ja", false},
		{"nothing set", "", "", false},
		{"POSIX default carries no signal", "POSIX", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			withOSLocale(t, tc.locale, "")
			code, available := detectLanguage()
			if code != tc.wantCode || available != tc.wantAvailable {
				t.Errorf("detectLanguage() = (%q, %v), want (%q, %v)", code, available, tc.wantCode, tc.wantAvailable)
			}
		})
	}
}
