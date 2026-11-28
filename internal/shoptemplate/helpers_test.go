package shoptemplate

import (
	"strings"
	"testing"
	"testing/fstest"
)

// fakeCatalog is the injected Catalog for unit tests: the real one (slots,
// registry, en.json) is wired in internal/pages (G5).
type fakeCatalog struct {
	keys    map[Kind][]string
	fixed   map[string]bool // "kind|key"
	normal  map[string]bool // registry tier normal
	caps    map[string]bool
	enKeys  map[string]bool
	allKeys bool
}

func newFakeCatalog() *fakeCatalog {
	return &fakeCatalog{
		keys: map[Kind][]string{
			KindSettings: {"settings-theme", "settings-printer", "settings-tills", "settings-payments", "settings-menulayout"},
			KindMenu:     {"/tables", "/kitchen-stations", "/items", "/settings"},
			KindItems:    {"/catalog"},
			KindRail:     {"/orders", "/menu"},
		},
		fixed:  map[string]bool{"settings|settings-payments": true, "menu|/settings": true, "rail|/menu": true},
		normal: map[string]bool{"sale.order_type_prompt": true, "sale.display_no_scheme": true, "sale.browsing_mode": true},
		caps:   map[string]bool{"printing": true, "ai": true},
		enKeys: map[string]bool{
			"lbl": true, "sum": true, "setup.shop_type.cafe": true, "g.hw": true, "lbl.services": true,
		},
	}
}

func (f *fakeCatalog) HasKey(k Kind, key string) bool {
	if f.allKeys {
		return true
	}
	for _, v := range f.keys[k] {
		if v == key {
			return true
		}
	}
	return false
}
func (f *fakeCatalog) FixedClass(k Kind, key string) bool { return f.fixed[string(k)+"|"+key] }
func (f *fakeCatalog) RegistryTier(key string) (string, bool) {
	if f.normal[key] {
		return "normal", true
	}
	if key == "sale.sensitive" {
		return "sensitive", true
	}
	return "", false
}
func (f *fakeCatalog) CapabilityKnown(c string) bool { return f.caps[c] }
func (f *fakeCatalog) EnKey(k string) bool           { return f.enKeys[k] }

// tmpl renders a minimal valid template JSON with the given extra members
// (already-formatted `"k":v` text, comma-joined by the caller).
func tmpl(id string, shopTypes []string, extra string) string {
	st := `"` + strings.Join(shopTypes, `","`) + `"`
	s := `{"schema":1,"id":"` + id + `","version":1,"shop_types":[` + st + `],"label_key":"lbl","summary_key":"sum"`
	if extra != "" {
		s += "," + extra
	}
	return s + "}"
}

// allTypesFS returns an fs with a valid template per shop type; tests
// override single files via the overrides map (file name -> content).
func allTypesFS(overrides map[string]string) fstest.MapFS {
	m := fstest.MapFS{}
	for _, t := range []string{"retail", "cafe", "hospitality", "service", "market_stall", "other"} {
		m["templates/"+t+".json"] = &fstest.MapFile{Data: []byte(tmpl(t, []string{t}, ""))}
	}
	for name, body := range overrides {
		if body == "" {
			delete(m, "templates/"+name+".json")
			continue
		}
		m["templates/"+name+".json"] = &fstest.MapFile{Data: []byte(body)}
	}
	return m
}

func mustLoad(t *testing.T, overrides map[string]string) *Set {
	t.Helper()
	s, err := loadFS(allTypesFS(overrides), newFakeCatalog())
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	return s
}

func loadErr(t *testing.T, overrides map[string]string) error {
	t.Helper()
	_, err := loadFS(allTypesFS(overrides), newFakeCatalog())
	if err == nil {
		t.Fatalf("load succeeded, want an error")
	}
	return err
}

func wantErrContains(t *testing.T, err error, sub string) {
	t.Helper()
	if !strings.Contains(err.Error(), sub) {
		t.Fatalf("error %q does not contain %q", err.Error(), sub)
	}
}
