package pages

import (
	"regexp"
	"strings"
	"testing"
)

// ut-docs#3050: the upright-tablet basket line is a container query on
// `.pos-container > .basket`. Current engines give `container-type:
// inline-size` no layout containment, but engines from before the 2023
// CSSWG change (Safari <= 16, older WebKitGTK) make a container the
// containing block for its `position: fixed` descendants -- the same
// hazard TestAppCSSNamesOnlyTheFixedRailAndStatusbar guards for
// view-transition-name (#2338). The basket's own fixed descendant (the
// shrinkage sheet) was measured centred on the viewport with the container
// in place; any other container needs the same check before it is added.
func TestAppCSSContainerOnlyOnTheBasket(t *testing.T) {
	css := readAppCSS(t)
	decl := regexp.MustCompile(`(?m)^\s*([^/\n{}]+)\{[^{}]*\bcontainer(-type)?\s*:`)
	found := decl.FindAllStringSubmatch(css, -1)
	if len(found) == 0 {
		t.Fatalf("app.css declares no container -- the #3050 basket container query has nothing to match")
	}
	for _, m := range found {
		if sel := strings.TrimSpace(m[1]); sel != ".pos-container > .basket" {
			t.Errorf("app.css makes %q a CSS container; only .pos-container > .basket may be one (older engines make a container the containing block for its fixed-position dialogs)", sel)
		}
	}
}
