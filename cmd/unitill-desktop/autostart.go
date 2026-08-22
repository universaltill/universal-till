// Pure autostart-entry logic (ut-docs#611 review fix) — deliberately free
// of any cgo/OS build tag, like window_mode.go, so `go test ./...` (which
// never sets `-tags desktop`, see stub.go) actually exercises it.
package main

import "strings"

// autostartEntryContents builds an XDG Desktop Entry that launches
// execPath — the till desktop shell's OWN executable, never the headless
// server it spawns (that was the review's Major finding M2: the entry must
// name unitill-desktop, the process with a window, not unitill-pos, the
// process it launches as a child). Mirrors the fields the shipped
// applications-menu entry already uses (packaging/linux/universal-till.desktop)
// so the till looks the same in "Startup Applications" as it does in the
// app menu.
//
// No Path= key: unlike a plain XDG launch (which starts with cwd=$HOME,
// where no web/ exists), unitill-desktop already resolves its own working
// directory relative to its executable (desktop.go's dirHasWeb fallback
// chain) — the same reason the shipped .desktop file above has no Path=
// either.
func autostartEntryContents(execPath string) string {
	var b strings.Builder
	b.WriteString("[Desktop Entry]\n")
	b.WriteString("Type=Application\n")
	b.WriteString("Name=Universal Till\n")
	b.WriteString("Comment=Offline-first point of sale\n")
	b.WriteString("Exec=" + quoteExec(execPath) + "\n")
	b.WriteString("Icon=universal-till\n")
	b.WriteString("Terminal=false\n")
	b.WriteString("Categories=Office;Finance;\n")
	b.WriteString("StartupNotify=false\n")
	return b.String()
}

// quoteExec applies the Desktop Entry spec's own escaping rules for an Exec
// value: a literal '%' must be doubled (or it's parsed as a field code),
// and a value containing whitespace or another reserved character must be
// wrapped in double quotes. unitill-desktop takes no arguments, so this
// only ever needs to quote/escape the single executable path — no
// argument-splitting logic to get wrong.
func quoteExec(execPath string) string {
	escaped := strings.ReplaceAll(execPath, "%", "%%")
	if strings.ContainsAny(escaped, " \t\"'\\$`") {
		escaped = `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`, "$", "\\$", "`", "\\`").Replace(escaped) + `"`
	}
	return escaped
}
