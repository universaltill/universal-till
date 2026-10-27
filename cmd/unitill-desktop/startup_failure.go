package main

// The startup-failure page (ut-docs#5021). When the spawned till server
// exits before it ever listens, the shell used to open its window on the dead
// server URL — a blank or browser-error page with no guidance. This page is
// shown instead: what happened, what to do, and where the logs are.
//
// Deliberately free of the `desktop` build tag, like child_wait.go, so plain
// `go test ./...` covers the language choice, the translator and the HTML.
// The shell has no HTTP server (and so no httpx.T): translations come from
// the locale files embedded in the binary via web/locales.FS, with the same
// per-key English fallback as cmd/unitill-uninstall/locale.go.

import (
	"bytes"
	"encoding/json"
	"html/template"
	"io/fs"
	"strings"
)

// startupFailureKeys are every locale key the page prints, in page order.
// startup_failure_test.go ties them to every embedded locale; keep this list
// in sync with the template below.
var startupFailureKeys = []string{
	"desktop.startup_failed.title",
	"desktop.startup_failed.body",
	"desktop.startup_failed.step_retry",
	"desktop.startup_failed.step_reinstall",
	"desktop.startup_failed.logs",
	"desktop.startup_failed.detail",
}

// shellLang picks the page language: the first non-empty primary subtag of
// LC_ALL, LC_MESSAGES, LANG ("tr_TR.UTF-8" -> "tr"; "C"/"POSIX" are not a
// language), else platformPreferred's primary subtag (a macOS GUI app usually
// has no LANG), else "en".
func shellLang(getenv func(string) string, platformPreferred string) string {
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if l := primarySubtag(getenv(k)); l != "" {
			return l
		}
	}
	if l := primarySubtag(platformPreferred); l != "" {
		return l
	}
	return "en"
}

// firstUILanguage returns the first non-blank entry of an OS-ordered UI
// language list ("tr-TR"), trimmed; "" when there is none (ut-docs#5051).
func firstUILanguage(langs []string) string {
	for _, l := range langs {
		if l = strings.TrimSpace(l); l != "" {
			return l
		}
	}
	return ""
}

// primarySubtag reduces "tr_TR.UTF-8", "ar-EG" or "en" to the lowercase
// language code; "" for empty input and the C/POSIX locales.
func primarySubtag(locale string) string {
	locale = strings.ToLower(strings.TrimSpace(locale))
	if i := strings.IndexAny(locale, "._-@"); i >= 0 {
		locale = locale[:i]
	}
	if locale == "c" || locale == "posix" {
		return ""
	}
	return locale
}

// loadShellTranslator never fails. en.json is the base; lang overlays it key
// by key (an empty value counts as missing); a key neither has is returned as
// itself. lang is external input (the environment), so only a plain
// lowercase code may reach the file system.
func loadShellTranslator(fsys fs.FS, lang string) func(string) string {
	base := readLocaleJSON(fsys, "en.json")
	var overlay map[string]string
	if lang != "" && lang != "en" && isPlainShellLang(lang) {
		overlay = readLocaleJSON(fsys, lang+".json")
	}
	return func(key string) string {
		if v := overlay[key]; v != "" {
			return v
		}
		if v := base[key]; v != "" {
			return v
		}
		return key
	}
}

func readLocaleJSON(fsys fs.FS, name string) map[string]string {
	raw, err := fs.ReadFile(fsys, name)
	if err != nil {
		return map[string]string{}
	}
	m := map[string]string{}
	if err := json.Unmarshal(raw, &m); err != nil {
		return map[string]string{}
	}
	return m
}

func isPlainShellLang(lang string) bool {
	for _, r := range lang {
		if r < 'a' || r > 'z' {
			return false
		}
	}
	return len(lang) > 0 && len(lang) <= 8
}

// textDir is the writing direction of a locale: Arabic, Persian and Hebrew
// are right-to-left.
func textDir(lang string) string {
	switch lang {
	case "ar", "fa", "he":
		return "rtl"
	}
	return "ltr"
}

var startupFailureTmpl = template.Must(template.New("startup_failure").Parse(`<!doctype html>
<html lang="{{.Lang}}" dir="{{.Dir}}">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Title}}</title>
<style>
html { background: #f6f7f9; }
body {
  margin: 0;
  padding: 32px 24px;
  font-family: system-ui, -apple-system, "Segoe UI", Roboto, "Noto Sans", "Helvetica Neue", Arial, sans-serif;
  font-size: 18px;
  line-height: 1.5;
  color: #1c2430;
  background: #f6f7f9;
  text-align: start;
}
main { max-inline-size: 640px; margin-inline: auto; }
h1 { font-size: 28px; line-height: 1.25; margin-block: 0 16px; }
p { margin-block: 0 16px; }
ol { margin-block: 0 24px; padding-inline-start: 1.5em; }
li { margin-block-end: 8px; }
code {
  display: block;
  padding: 12px 14px;
  margin-block: 0 24px;
  background: #fff;
  border: 1px solid #c9d0da;
  border-radius: 6px;
  font-family: ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
  font-size: 15px;
  overflow-wrap: anywhere;
  direction: ltr;
  text-align: start;
  user-select: all;
  -webkit-user-select: all;
}
.detail { font-size: 14px; color: #5a6575; overflow-wrap: anywhere; }
@media (max-width: 480px) {
  body { padding: 20px 16px; font-size: 16px; }
  h1 { font-size: 24px; }
}
</style>
</head>
<body>
<main>
<h1>{{.Title}}</h1>
<p>{{.Body}}</p>
<ol>
<li>{{.StepRetry}}</li>
<li>{{.StepReinstall}}</li>
</ol>
<p>{{.Logs}}</p>
<code>{{.DataDir}}</code>
{{if .Reason}}<p class="detail">{{.Detail}}: {{.Reason}}</p>
{{end}}</main>
</body>
</html>
`))

// startupFailurePage renders the complete, self-contained HTML document
// (inline CSS, no external resources) shown when the till server exited
// during startup. dataDir is the folder holding desktop.log and till.log;
// reason is the child's exit error ("signal: killed"), omitted when empty.
// html/template escapes dataDir and reason.
func startupFailurePage(tr func(string) string, lang, dataDir, reason string) string {
	var buf bytes.Buffer
	err := startupFailureTmpl.Execute(&buf, map[string]string{
		"Lang":          lang,
		"Dir":           textDir(lang),
		"Title":         tr("desktop.startup_failed.title"),
		"Body":          tr("desktop.startup_failed.body"),
		"StepRetry":     tr("desktop.startup_failed.step_retry"),
		"StepReinstall": tr("desktop.startup_failed.step_reinstall"),
		"Logs":          tr("desktop.startup_failed.logs"),
		"Detail":        tr("desktop.startup_failed.detail"),
		"DataDir":       dataDir,
		"Reason":        reason,
	})
	if err != nil {
		// Only possible on a template bug; a plain page still beats none.
		return "<!doctype html><meta charset=\"utf-8\"><p>" + template.HTMLEscapeString(tr("desktop.startup_failed.title")) + "</p>"
	}
	return buf.String()
}

// availableShellLang is lang when the embedded locales have a file for it,
// else "en": the page's lang/dir attributes must describe the text actually
// shown, so a Hebrew system (no he.json, English text) is not laid out
// right-to-left.
func availableShellLang(fsys fs.FS, lang string) string {
	if lang == "en" || !isPlainShellLang(lang) {
		return "en"
	}
	if _, err := fs.Stat(fsys, lang+".json"); err != nil {
		return "en"
	}
	return lang
}
