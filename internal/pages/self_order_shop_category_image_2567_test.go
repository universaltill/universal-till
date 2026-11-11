package pages

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/data"
	"github.com/universaltill/universal-till/internal/paths"
	"github.com/universaltill/universal-till/internal/ui"
)

// ut-docs#2567: the self-order kiosk's category chips show the category's
// picture/icon with the same rule as the sell screen (ui.CategoryThumb).

func writeUpload(t *testing.T, dir, rel string) {
	t.Helper()
	p := filepath.Join(dir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte("png"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestKioskCategoryChips_ParityWithCategoryThumb(t *testing.T) {
	dir := useTempDataDir(t)
	writeUpload(t, dir, "public/assets/categories/c-photo/thumb.png")
	writeUpload(t, dir, "public/assets/categories/c-both/thumb.png")

	nodes := []data.CategoryNode{
		{ID: "c-photo", Name: "Photo", ImagePath: "/public/assets/categories/c-photo/thumb.png"},
		{ID: "c-icon", Name: "Icon", Icon: "lucide:beer"},
		{ID: "c-both", Name: "Both", ImagePath: "/public/assets/categories/c-both/thumb.png", Icon: "lucide:beer"},
		{ID: "c-none", Name: "None"},
		{ID: "c-legacy", Name: "Legacy", ImagePath: "/public/assets/category-icons/coffee.svg"},
		{ID: "c-unknown", Name: "Unknown", Icon: "nope:does-not-exist"},
		{ID: "c-missing", Name: "Missing", ImagePath: "/public/assets/categories/c-missing/thumb.png"},
		{ID: "c-etc", Name: "Etc", ImagePath: "/etc/passwd"},
		{ID: "c-dots", Name: "Dots", ImagePath: "/public/../x"},
	}
	chips := kioskCategoryChips(nodes)
	if len(chips) != len(nodes) {
		t.Fatalf("got %d chips, want %d", len(chips), len(nodes))
	}
	for i, n := range nodes {
		want := ui.CategoryThumb(n.ImagePath, n.Icon)
		if chips[i].ID != n.ID || chips[i].Name != n.Name {
			t.Errorf("chip %d = %+v, want id/name of %+v", i, chips[i], n)
		}
		if chips[i].ImageURL != want {
			t.Errorf("%s: ImageURL = %q, want CategoryThumb %q", n.ID, chips[i].ImageURL, want)
		}
	}
	// Sanity: the fixtures really exercise both a picture and "no picture".
	if chips[0].ImageURL == "" || chips[3].ImageURL != "" || chips[6].ImageURL != "" {
		t.Fatalf("fixture sanity failed: photo=%q none=%q missing=%q", chips[0].ImageURL, chips[3].ImageURL, chips[6].ImageURL)
	}
}

func kioskShopBody(t *testing.T, setup func(dir string, exec func(q string))) string {
	t.Helper()
	dir := useTempDataDir(t)
	dp, d := setupSelfOrderShopDeps(t)
	// setupSelfOrderShopDeps chdirs to the repo root; paths was set before,
	// re-assert it so uploads resolve against the temp dir.
	paths.Init(dir)
	setup(dir, func(q string) {
		if _, err := d.DB.Exec(q); err != nil {
			t.Fatal(err)
		}
	})
	mux := http.NewServeMux()
	registerSelfOrderShop(mux, dp)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/self-order/shop", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	return rec.Body.String()
}

func TestSelfOrderShop_ChipRendersImgBeforeName(t *testing.T) {
	body := kioskShopBody(t, func(dir string, exec func(string)) {
		writeUpload(t, dir, "public/assets/categories/cp/thumb.png")
		exec(`INSERT INTO categories (id, name, is_active, image_path) VALUES ('cp','Pastries',1,'/public/assets/categories/cp/thumb.png')`)
	})
	re := regexp.MustCompile(`<button type="button" class="cat-chip" data-cat="cp"><img class="cat-chip-img" src="(/public/assets/categories/cp/thumb\.png[^"]*)" alt="" loading="lazy">Pastries</button>`)
	if !re.MatchString(body) {
		t.Fatalf("photo chip markup not found in:\n%s", chipStrip(body))
	}
	if strings.Contains(chipStrip(body), "aria-label=\"Pastries") {
		t.Fatal("chip must not carry an aria-label duplicating its text")
	}
}

func TestSelfOrderShop_ChipWithoutImageIsTextOnly(t *testing.T) {
	body := kioskShopBody(t, func(dir string, exec func(string)) {
		exec(`INSERT INTO categories (id, name, is_active) VALUES ('cn','Plain',1)`)
		// photo file missing on this till -> text-only, never a broken img
		exec(`INSERT INTO categories (id, name, is_active, image_path) VALUES ('cm','Gone',1,'/public/assets/categories/cm/thumb.png')`)
	})
	for _, want := range []string{
		`<button type="button" class="cat-chip" data-cat="cn">Plain</button>`,
		`<button type="button" class="cat-chip" data-cat="cm">Gone</button>`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("missing text-only chip %q in:\n%s", want, chipStrip(body))
		}
	}
	if strings.Contains(chipStrip(body), "<img") {
		t.Fatalf("no chip should carry an <img>:\n%s", chipStrip(body))
	}
	if !strings.Contains(body, `<button type="button" class="cat-chip active" data-cat="">`) {
		t.Fatal("All chip changed or missing")
	}
}

func TestSelfOrderShop_ChipsListActiveOnlyInSortOrder(t *testing.T) {
	body := kioskShopBody(t, func(dir string, exec func(string)) {
		exec(`INSERT INTO categories (id, name, is_active, sort_order) VALUES ('s1','Zebra',1,1),('s2','Apple',1,2),('s3','Retired',0,0)`)
	})
	if strings.Contains(body, `data-cat="s3"`) {
		t.Fatal("deactivated category must not get a chip")
	}
	z, a := strings.Index(body, `data-cat="s1"`), strings.Index(body, `data-cat="s2"`)
	if z < 0 || a < 0 || z > a {
		t.Fatalf("chips must follow sort_order (Zebra before Apple): z=%d a=%d", z, a)
	}
}

func TestSelfOrderShop_ChipImageNeverExternal(t *testing.T) {
	body := kioskShopBody(t, func(dir string, exec func(string)) {
		exec(`INSERT INTO categories (id, name, is_active, image_path) VALUES ('e1','Ext',1,'https://evil.example/x.png'),('e2','Icon',1,'/public/assets/category-icons/coffee.svg')`)
	})
	strip := chipStrip(body)
	if strings.Contains(strip, "evil.example") {
		t.Fatalf("external URL leaked into chips:\n%s", strip)
	}
	imgs := regexp.MustCompile(`<img class="cat-chip-img" src="([^"]*)"`).FindAllStringSubmatch(strip, -1)
	if len(imgs) == 0 {
		t.Fatalf("expected the built-in icon chip to render an img:\n%s", strip)
	}
	for _, m := range imgs {
		if !strings.HasPrefix(m[1], "/public/") {
			t.Errorf("chip src %q is not a local /public/ asset", m[1])
		}
	}
}

func TestSelfOrderShop_ChipImageCSSIsLogical(t *testing.T) {
	body := kioskShopBody(t, func(string, func(string)) {})
	i := strings.Index(body, ".cat-chip-img")
	if i < 0 {
		t.Fatal("expected a .cat-chip-img rule in the page style")
	}
	var rules []string
	for _, m := range regexp.MustCompile(`[^{}]*\.cat-chip[^{}]*\{[^}]*\}`).FindAllString(body, -1) {
		rules = append(rules, m)
	}
	css := strings.Join(rules, "\n")
	if !strings.Contains(css, "inline-size") {
		t.Fatalf("chip image sized with inline-size expected:\n%s", css)
	}
	if regexp.MustCompile(`(?i)\b(left|right|margin-left|margin-right|padding-left|padding-right)\b`).MatchString(css) {
		t.Fatalf("physical left/right in chip css:\n%s", css)
	}
}

// chipStrip returns just the .selforder-cats block for readable failures.
func chipStrip(body string) string {
	i := strings.Index(body, `class="selforder-cats"`)
	if i < 0 {
		return body
	}
	j := strings.Index(body[i:], "</div>")
	if j < 0 {
		return body[i:]
	}
	return body[i : i+j]
}
