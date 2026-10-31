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
//
// ut-docs#5221: the sale screen's action row is the second container. Its
// children are four buttons and a hidden span (web/ui/pages/index.html,
// .tender-default-footer): no dialog or other fixed-position descendant, and
// the count badge is absolute inside its own position: relative button. The
// payment overlay and the popups the row opens are elsewhere in .tender, not
// inside it (overlay checked open at 768x400/600: full viewport, hit-testable).
var allowedCSSContainers = map[string]bool{
	".pos-container > .basket": true,
	".tender-default-footer":   true,
}

func TestAppCSSContainerOnlyOnTheBasket(t *testing.T) {
	css := readAppCSS(t)
	decl := regexp.MustCompile(`(?m)^\s*([^/\n{}]+)\{[^{}]*\bcontainer(-type)?\s*:`)
	found := decl.FindAllStringSubmatch(css, -1)
	if len(found) == 0 {
		t.Fatalf("app.css declares no container -- the #3050 basket container query has nothing to match")
	}
	for _, m := range found {
		if sel := strings.TrimSpace(m[1]); !allowedCSSContainers[sel] {
			t.Errorf("app.css makes %q a CSS container; only .pos-container > .basket and .tender-default-footer may be one (older engines make a container the containing block for its fixed-position dialogs -- check its fixed descendants, then add it to allowedCSSContainers)", sel)
		}
	}
}
