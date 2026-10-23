package catimport

import (
	"strings"
	"testing"
)

// Review R4 (ut-docs#4840): the tile generator must refuse anything but
// plain drawing primitives with a safe attribute set.
func TestIconSourceChildren_Allowlist(t *testing.T) {
	wrap := func(body string) []byte {
		return []byte(`<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 24 24">` + body + `</svg>`)
	}
	ok := wrap(`<path d="M1 1h4"/><circle cx="3" cy="3" r="1"/><rect x="1" y="1" width="2" height="2" rx="1"/>` +
		`<line x1="1" y1="1" x2="2" y2="2"/><polyline points="1,1 2,2"/><polygon points="1,1 2,2 3,1"/><ellipse cx="1" cy="1" rx="1" ry="2"/>`)
	if got, err := iconSourceChildren(ok); err != nil || len(got) != 7 {
		t.Fatalf("clean glyph: got %d elements, err %v", len(got), err)
	}
	bad := map[string]string{
		"script element":     `<path d="M1 1"/><script>alert(1)</script>`,
		"script self-close":  `<script src="x.js"/>`,
		"image":              `<image href="https://x/y.png"/>`,
		"use":                `<use href="#a"/>`,
		"style element":      `<style>path{fill:red}</style><path d="M1 1"/>`,
		"group wrapper":      `<g><path d="M1 1"/></g>`,
		"foreignObject":      `<foreignObject width="1" height="1"/>`,
		"onload handler":     `<path d="M1 1" onload="x()"/>`,
		"href attribute":     `<path d="M1 1" href="javascript:x()"/>`,
		"style attribute":    `<path d="M1 1" style="fill:red"/>`,
		"url() reference":    `<path d="M1 1" fill="url(#a)"/>`,
		"unquoted attribute": `<path d="M1 1" fill=red/>`,
		"markup in value":    `<path d="M1 1" stroke="&lt;x"/>`,
	}
	for name, body := range bad {
		if _, err := iconSourceChildren(wrap(body)); err == nil {
			t.Errorf("%s: accepted, want an error", name)
		} else if strings.TrimSpace(err.Error()) == "" {
			t.Errorf("%s: empty error", name)
		}
	}
}
