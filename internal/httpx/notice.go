package httpx

import (
	"fmt"
	"html/template"
	"io"
	"strings"
)

// RenderNotice writes the ut-docs#213 .pos-notice markup
// (docs/sale-screen-notifications.md) — the one message surface the sale
// screen uses (web/ui/partials/basket.html renders it inline) — for a
// handler whose HTTP response fragment IS a notice: an htmx
// hx-target="#some-msg" swap that isn't itself a bigger page/table
// fragment. ut-docs#238 extends the pattern to those non-sale-screen
// message spots.
//
//	<div class="pos-notice LEVEL" role="ROLE">
//	  <span class="notice-text">MESSAGE</span>
//	  <button type="button" class="notice-dismiss" aria-label="DISMISS_LABEL">✕</button>
//	</div>
//
// level is "error" | "success" | "info": "error" renders role="alert" and
// persists until the operator dismisses it; anything else renders
// role="status" and auto-expires after ~2.5s (scheduleToastDismiss in
// web/public/app.js). message is httpx.T(locale, key).
//
// The translated message is HTML-escaped. Every other place a translation
// reaches a page is a {{ T "key" }} inside an html/template, which escapes
// it automatically; this helper writes straight to the ResponseWriter, so
// it has to do that itself. It matters because locale VALUES legitimately
// contain HTML-significant characters ("Help & Support",
// `... "Ask me later" ...`), and because a locale can come from a
// third-party language-pack plugin (ADR-0009) rather than this repo.
//
// extra, when given, is additional HTML appended after the translated
// message, space-separated — for a caller that needs to interpolate
// non-translatable content the message itself doesn't carry (a filesystem
// path, a copies count), the same thing the fmt.Fprintf call sites this
// helper replaces used to do. Unlike the message, extra is written as-is,
// not escaped — it is markup by contract (`<code>…</code>`), so a caller
// interpolating anything user-controlled into it must escape that itself.
func RenderNotice(w io.Writer, locale, level, key string, extra ...string) {
	msg := template.HTMLEscapeString(T(locale, key))
	if len(extra) > 0 {
		msg = msg + " " + strings.Join(extra, " ")
	}
	role := "status"
	if level == "error" {
		role = "alert"
	}
	dismissLabel := template.HTMLEscapeString(T(locale, "notice.dismiss"))
	fmt.Fprintf(w, `<div class="pos-notice %s" role="%s"><span class="notice-text">%s</span><button type="button" class="notice-dismiss" aria-label="%s">✕</button></div>`,
		level, role, msg, dismissLabel)
}
