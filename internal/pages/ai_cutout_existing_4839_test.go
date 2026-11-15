package pages

// ut-docs#4839: "Remove background" on the photo a record ALREADY has. The
// item form and the category dialog render a button next to the current
// photo (shown by the page script only for an uploaded photo, never a
// built-in icon); it re-offers the saved photo to web/public/photo-cutout.js.
// Rendered exactly when the offer panel is: for a catalog_management holder.

import (
	"net/http"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
	"github.com/universaltill/universal-till/internal/data"
)

var existingCutoutButtons = []struct {
	name, path, marker string
}{
	{"item form", "/catalog", `id="image-current-cutout-btn"`},
	{"category form", "/categories", `id="category-image-current-cutout-btn"`},
}

func TestCutoutExisting_ButtonRenderedForCatalogManagers(t *testing.T) {
	t.Setenv("UT_AUTH", "off")
	mux, _ := newCutoutOfferMux(t)
	for _, p := range existingCutoutButtons {
		t.Run(p.name, func(t *testing.T) {
			rec := cutoutGetAs(t, mux, p.path, nil)
			if rec.Code != http.StatusOK {
				t.Fatalf("expected 200, got %d", rec.Code)
			}
			body := rec.Body.String()
			i := strings.Index(body, p.marker)
			if i < 0 {
				t.Fatalf("expected the existing-photo button %s", p.marker)
			}
			// Hidden until the page script sees an uploaded photo, a
			// secondary button, the existing translated label.
			tag := body[strings.LastIndex(body[:i], "<button"):]
			tag = tag[:strings.Index(tag, "</button>")]
			for _, want := range []string{"hidden", `class="btn secondary"`, "Remove background"} {
				if !strings.Contains(tag, want) {
					t.Errorf("button %q missing %q", tag, want)
				}
			}
		})
	}
}

func TestCutoutExisting_ButtonAbsentWithoutCatalogManagement(t *testing.T) {
	t.Setenv("UT_AUTH", "")
	mux, dp := newCutoutOfferMux(t)
	dp.AuthSvc = auth.NewService(dp.Db)
	dp.AI, _ = fakeRembg(t, http.StatusOK, cutoutPNG(t))
	authRepo := data.NewAuthRepo(dp.Db)
	cashierID, err := authRepo.CreateUser(t.Context(), "exist-cashier", "Cashier", "cashier")
	if err != nil {
		t.Fatal(err)
	}
	managerID, err := authRepo.CreateUser(t.Context(), "exist-manager", "Manager", "manager")
	if err != nil {
		t.Fatal(err)
	}
	cashier := auth.User{ID: cashierID, Role: "cashier"}
	manager := auth.User{ID: managerID, Role: "manager"}
	for _, p := range existingCutoutButtons {
		t.Run(p.name, func(t *testing.T) {
			if rec := cutoutGetAs(t, mux, p.path, &cashier); strings.Contains(rec.Body.String(), p.marker) {
				t.Fatalf("cashier was shown %s", p.marker)
			}
			rec := cutoutGetAs(t, mux, p.path, &manager)
			if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), p.marker) {
				t.Fatalf("manager: expected %s, got %d", p.marker, rec.Code)
			}
		})
	}
}
