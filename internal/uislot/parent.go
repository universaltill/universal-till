package uislot

import "strings"

// adminGroup is the Menu-slot Group of the destinations that live behind
// the Administration tile (/admin) instead of on the Menu itself.
const adminGroup = "menu.group.administration"

// ParentOf is the page the shell's Back returns to when there is no in-app
// page to go back to (a cold start, a reload of the first page, a deep
// link) — ADR-0137, ut-docs#3352. It is read from the slot tables above,
// never from a hand-kept route list, so a new tile or Items section gets
// the right parent for free:
//
//   - "/" (Sell) is the root: "" — no Back at all.
//   - "/menu" returns to Sell.
//   - an Items section (CoreItems) returns to /items;
//   - an Administration destination (adminGroup) returns to /admin;
//   - any other Menu tile returns to /menu;
//   - a sub-page returns to the nearest declared page above it
//     (/settings/printers → /settings, /catalog/42/edit → /catalog);
//   - anything else — a plugin page, an unknown route — returns to /menu.
//
// Plugin amendments (ADR-0088) can relabel, reorder or hide entries but
// never change a core entry's Href, so the core tables are enough here.
func ParentOf(path string) string {
	path = strings.TrimSuffix(path, "/")
	switch path {
	case "":
		return ""
	case "/menu":
		return "/"
	}
	if p, ok := declaredParent(path); ok {
		return p
	}
	// Walk up to the nearest declared page above this one.
	for p := path; ; {
		i := strings.LastIndexByte(p, '/')
		if i <= 0 {
			break
		}
		p = p[:i]
		if _, ok := declaredParent(p); ok {
			return p
		}
	}
	return "/menu"
}

// declaredParent is the parent of a page the slot tables declare, and
// whether they declare it at all.
func declaredParent(path string) (string, bool) {
	if _, ok := coreItemsIndex[path]; ok {
		return "/items", true
	}
	if e, ok := CoreMenuEntry(path); ok {
		if e.Group == adminGroup {
			return "/admin", true
		}
		return "/menu", true
	}
	if _, ok := CoreRailEntry(path); ok {
		return "/menu", true
	}
	return "", false
}
