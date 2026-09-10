package pages

import (
	"regexp"
	"strings"
	"testing"

	"github.com/universaltill/universal-till/internal/auth"
)

// ut-docs#1960: Settings as a two-pane master-detail (section list + search
// on the inline-start side, the selected section's card on the other). The
// whole change is template + CSS + a page-local script over the cards this
// template already renders — settings_page.go is untouched, and these tests
// pin the server-rendered contract that script relies on:
//
//   - the shell it mounts into is present (#settings-shell / #settings-nav /
//     #settings-panel / #settings-tree), so the page never degrades back to
//     an all-at-once column dump without anyone noticing;
//   - EVERY rendered card carries an id, because the section list is built
//     from the DOM and a card with no id is a section nobody can deep-link
//     to (the script would synthesise one, but then base.html-style links
//     could never target it);
//   - the two load-bearing inbound deep links survive: base.html's
//     /settings#registration (the card's own id) and update_api.go's
//     /settings#android-update (a <span> nested INSIDE the Software update
//     card, which now carries id="settings-update" so the script can
//     resolve the nested anchor to its section — ut-docs#1534's own
//     regression guards in android_update_placement_test.go stay
//     unmodified and must keep passing alongside these).

var cardOpenTag = regexp.MustCompile(`<div class="card[^"]*"[^>]*>`)

func TestSettingsTwoPaneShellRenders(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	body := renderSettingsAs(t, mux, mgrUser, true)

	for _, want := range []string{
		`id="settings-shell"`,
		`id="settings-nav"`,
		`id="settings-panel"`,
		`id="settings-tree"`,
		`id="settings-q"`,
		`id="settings-noresults"`,
		`id="settings-back"`,
		`id="settings-grid"`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("settings two-pane shell is missing %s (ut-docs#1960)", want)
		}
	}

	// The section-switcher script has to run AFTER every card exists in the
	// DOM — it builds the nav from them — so it must follow the grid's close.
	// Located by a code token rather than its header comment: html/template
	// strips JS comments inside <script> when it renders.
	gridStart := strings.Index(body, `id="settings-grid"`)
	script := strings.LastIndex(body, `classList.toggle('settings-section-hidden'`)
	if gridStart < 0 || script < 0 || script < gridStart {
		t.Errorf("the two-pane section switcher script must come after #settings-grid (grid at %d, script at %d)", gridStart, script)
	}
	lastCard := strings.LastIndex(body, `<div class="card`)
	if script < lastCard {
		t.Errorf("the two-pane section switcher script (%d) runs before the last settings card (%d) exists in the DOM", script, lastCard)
	}
}

func TestSettingsEveryCardHasAStableID(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)

	// Both roles: the cashier render drops the manager-only cards, and the
	// manager render is the full set. Every card that survives either gate
	// must be addressable.
	idAttr := regexp.MustCompile(` id="([^"]+)"`)
	for _, tc := range []struct {
		name string
		user auth.User
	}{
		{"manager", mgrUser},
		{"cashier", cashUser},
	} {
		name := tc.name
		body := renderSettingsAs(t, mux, tc.user, true)
		gridStart := strings.Index(body, `id="settings-grid"`)
		if gridStart < 0 {
			t.Fatalf("%s: no #settings-grid rendered", name)
		}
		tags := cardOpenTag.FindAllString(body[gridStart:], -1)
		if len(tags) < 10 {
			t.Fatalf("%s: only %d settings cards rendered — expected the full settings page", name, len(tags))
		}
		seen := map[string]bool{}
		for _, tag := range tags {
			m := idAttr.FindStringSubmatch(tag)
			if m == nil {
				t.Errorf("%s: settings card without an id — unreachable from the section list and un-deep-linkable (ut-docs#1960): %s", name, tag)
				continue
			}
			if seen[m[1]] {
				t.Errorf("%s: duplicate settings card id %q — two sections would collapse into one nav entry", name, m[1])
			}
			seen[m[1]] = true
		}
		// The two inbound deep-link targets that exist today.
		if !seen["registration"] {
			t.Errorf("%s: the registration card lost id=\"registration\" — base.html links to /settings#registration", name)
		}
		if !seen["settings-update"] {
			t.Errorf("%s: the Software update card has no id=\"settings-update\" — the #android-update nested anchor needs a section to resolve to", name)
		}
	}
}

// The status chip's /settings#android-update anchor is NESTED inside the
// Software update card, not a card id of its own. The script resolves that
// by walking up to the enclosing card, which only works if that card is the
// one carrying id="settings-update" — assert the containment positionally.
func TestAndroidUpdateAnchorNestsInsideTheSettingsUpdateCard(t *testing.T) {
	mux, _, _ := newFullAuthDeps(t)
	body := renderSettingsAs(t, mux, mgrUser, true)

	anchor := strings.Index(body, `id="android-update"`)
	if anchor < 0 {
		t.Fatal("no #android-update anchor rendered on an Android till with an update available")
	}
	cardStart := strings.LastIndex(body[:anchor], `<div class="card`)
	if cardStart < 0 {
		t.Fatal("the #android-update anchor is not inside any settings card")
	}
	openTag := cardOpenTag.FindString(body[cardStart:])
	if !strings.Contains(openTag, `id="settings-update"`) {
		t.Errorf("the card enclosing #android-update must be id=\"settings-update\" so /settings#android-update opens the Software update section (ut-docs#1960); got %s", openTag)
	}
}
