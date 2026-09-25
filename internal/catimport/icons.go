package catimport

import "github.com/universaltill/universal-till/internal/iconid"

// Built-in category/item icon library (ut-docs#2506, first cut).
//
// Every icon is generated from a vendored upstream SVG under iconsrc/
// (Lucide — ISC, Tabler — MIT; both LICENSE files sit next to the SVGs)
// into a 64×64 tile at web/public/assets/category-icons/<key>.svg by the
// golden-file test in icons_render_test.go. TestCategoryIconTiles_InSync
// fails when a tile, this registry and its source drift, so the files are
// never hand-edited — change the registry or the source, then regenerate:
//
//	UPDATE_CATEGORY_ICONS=1 go test ./internal/catimport -run TestCategoryIconTiles_InSync
//
// (The generator lives in a _test.go file so the shipped binary carries
// no code it never runs — guard-deadcode-baseline.sh.)
//
// Why a baked tile and not a bare `currentColor` glyph like
// internal/httpx/icons.go: these icons render through `<img src>` (item
// tiles on the sell screen, the pickers) and are stored as that path in
// item_images/categories, and an <img> can't inherit currentColor. So
// each tile carries its group's soft background and dark stroke, the same
// look the original coffee tile (ut-docs#1189) had.
//
// Keys are plain kebab-case because the key is the filename inside the
// path older tills stored; Src is the upstream id ("lucide:beer"), which
// is also the category icon id a pick now stores (ut-docs#2717) and the
// id my. uses. A key, once shipped, is never renamed or removed: shops'
// stored paths point at it.

// iconDef is one registry entry. Src is "<set>:<name>" naming
// iconsrc/<set>/<name>.svg, or "" for a hand-drawn legacy tile that the
// generator leaves alone. Keywords are English search terms for the
// picker's filter box (the translated label is searched too).
type iconDef struct {
	Key, Src, Group string
	Keywords        []string
}

// iconGroup is one picker section: its tile colours and heading key.
type iconGroup struct {
	Key, Background, Stroke string
}

// iconGroups is the picker's section order.
var iconGroups = []iconGroup{
	{Key: "hot", Background: "#F2E8DD", Stroke: "#6F4E37"},
	{Key: "cold", Background: "#E0F2FE", Stroke: "#075985"},
	{Key: "bar", Background: "#EDE9FE", Stroke: "#5B21B6"},
	{Key: "bakery", Background: "#FEF3C7", Stroke: "#92400E"},
	{Key: "meals", Background: "#FFE4E6", Stroke: "#9F1239"},
	{Key: "produce", Background: "#DCFCE7", Stroke: "#166534"},
	{Key: "sweets", Background: "#FCE7F3", Stroke: "#9D174D"},
	{Key: "general", Background: "#E2E8F0", Stroke: "#334155"},
}

// iconDefs is the library (ut-docs#2506) as this package uses it. The
// list itself lives in internal/iconid (library.go) — ONE registry for the
// icon ids my./directives store and the tiles this package's pickers,
// generator and placeholder offer (ut-docs#2664). Src is the entry's icon
// id, which is also its vendored upstream glyph.
var iconDefs = func() []iconDef {
	lib := iconid.Library()
	out := make([]iconDef, 0, len(lib))
	for _, ic := range lib {
		out = append(out, iconDef{Key: ic.Key, Src: ic.ID, Group: ic.Group, Keywords: ic.Keywords})
	}
	return out
}()

// iconByKey indexes iconDefs; built once at init.
var iconByKey = func() map[string]iconDef {
	m := make(map[string]iconDef, len(iconDefs))
	for _, d := range iconDefs {
		m[d.Key] = d
	}
	return m
}()

// categoryIconPublicDir is where the tiles are served from ("/public/..."
// maps to web/public/ — the same convention the per-item uploads use).
const categoryIconPublicDir = iconid.PublicDir

// BuiltinIconGroup is one section of the picker: its heading's locale key
// and its icons in display order.
type BuiltinIconGroup struct {
	Key, I18nKey string
	Icons        []BuiltinIcon
}

// BuiltinIconGroups returns the library grouped for the pickers, in
// section order. Every icon in BuiltinIcons() appears in exactly one
// group.
func BuiltinIconGroups() []BuiltinIconGroup {
	byGroup := map[string][]BuiltinIcon{}
	for _, ic := range BuiltinIcons() {
		byGroup[ic.Group] = append(byGroup[ic.Group], ic)
	}
	out := make([]BuiltinIconGroup, 0, len(iconGroups))
	for _, g := range iconGroups {
		out = append(out, BuiltinIconGroup{
			Key:     g.Key,
			I18nKey: "catalog.builtin_icon.group." + g.Key,
			Icons:   byGroup[g.Key],
		})
	}
	return out
}
