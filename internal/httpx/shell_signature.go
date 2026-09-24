package httpx

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"strings"

	"github.com/universaltill/universal-till/internal/buildinfo"
)

// HeadAssets is every asset base.html's <head> loads with `?v={{ assetv … }}`
// — the persistent shell (ADR-0098) is exactly these files plus the theme
// stylesheet, so their versions are what the shell signature covers. A Go
// guard in internal/pages keeps this list equal to base.html's real tags.
var HeadAssets = []string{
	"public/app.css",
	"public/vendor/htmx.min.js",
	"public/vendor/alpine.min.js",
	"public/app.js",
	"public/osk.js",
	"public/cursor.js",
	"public/autofill.js",
	"public/input-heartbeat.js",
	"public/record-dialog.js",
	"public/category-filter.js",
	"public/icon-picker-filter.js", // ut-docs#2506: built-in icon picker search
	"public/bugreport-draft.js",    // ut-docs#2342: the bug-report draft store
}

// headAssetVersion is assetVersion, indirected so a test can prove the
// signature follows an asset version change without touching mtimes.
var headAssetVersion = assetVersion

// ShellSignature identifies what a base.html page's <head> and <html>
// attributes would render for this locale and theme: the app version,
// every head asset's version, the theme stylesheet's version, the locale
// and its text direction. Rendered as <meta name="ut-shell"> and compared
// by the boosted-navigation client code before it swaps a response into
// the live document (ut-docs#2224, ADR-0098): equal → swap the page region
// only; different or absent → full document load, which is what makes the
// first navigation after a self-update, a theme change or a language change
// safe, and keeps pages outside base.html (login, setup, self-order) on
// full loads.
func ShellSignature(locale, theme string) string {
	var b strings.Builder
	b.WriteString(buildinfo.Version)
	for _, rel := range HeadAssets {
		b.WriteByte('|')
		b.WriteString(headAssetVersion(rel))
	}
	if theme == "" {
		theme = "default"
	}
	b.WriteString("|theme=")
	b.WriteString(theme)
	b.WriteByte('|')
	b.WriteString(headAssetVersion("public/themes/" + theme + ".css"))
	// Shell-level settings that base.html renders outside the swapped
	// region or that a shell script reads once at boot (osk.js reads
	// data-osk, app.js data-idle-lock, <html style> carries --ui-scale):
	// a change forces the next navigation to be a full load.
	b.WriteString("|uiscale=")
	b.WriteString(uiScaleCSS())
	b.WriteString("|osk=")
	b.WriteString(oskModeVal())
	b.WriteString("|idle=")
	b.WriteString(strconv.FormatInt(idleLockSecs.Load(), 10))
	b.WriteString("|lang=")
	b.WriteString(strings.ToLower(locale))
	if IsRTL(locale) {
		b.WriteString("|rtl")
	} else {
		b.WriteString("|ltr")
	}
	sum := sha256.Sum256([]byte(b.String()))
	return hex.EncodeToString(sum[:8])
}
