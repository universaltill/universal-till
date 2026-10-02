package main

import "testing"

// webkit_navigation.go (ut-docs#372) is the pure policy behind the Linux
// shell's external-link routing — untagged so plain `go test ./...` covers
// it; webkit_recovery_linux.go only wires it onto WebKitGTK's decide-policy.

func TestIsExternalNav(t *testing.T) {
	const base = "http://127.0.0.1:8080"
	cases := []struct {
		name string
		uri  string
		want bool
	}{
		{"same origin page", "http://127.0.0.1:8080/sale", false},
		{"same origin root without path", "http://127.0.0.1:8080", false},
		{"same origin with query and fragment", "http://127.0.0.1:8080/x?y=1#z", false},
		{"other host (the update download link)", "https://www.universaltill.com/download", true},
		{"other host http", "http://example.com/", true},
		{"same host other port (another till)", "http://127.0.0.1:9090/plugins", true},
		{"same host other scheme", "https://127.0.0.1:8080/sale", true},
		{"localhost is not 127.0.0.1", "http://localhost:8080/sale", true},
		// Not http(s): nothing a browser should be handed, and not ours to
		// reroute (same rule as webkit_darwin.go's isExternalURL) — blob:
		// is how the EOD/plugin exports download, about:srcdoc is the
		// plugin page iframe.
		{"about:blank", "about:blank", false},
		{"about:srcdoc", "about:srcdoc", false},
		{"data uri", "data:text/html,%3Ch1%3Ehi%3C%2Fh1%3E", false},
		{"blob download", "blob:http://127.0.0.1:8080/6f1c2d1e-0000-4000-8000-000000000000", false},
		{"javascript uri", "javascript:void(0)", false},
		{"mailto", "mailto:support@example.com", false},
		{"empty uri", "", false},
		{"garbage uri", "http://[::1", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isExternalNav(base, tc.uri); got != tc.want {
				t.Fatalf("isExternalNav(%q, %q) = %v, want %v", base, tc.uri, got, tc.want)
			}
		})
	}
}

func TestIsExternalNav_DefaultPortsAndCase(t *testing.T) {
	if isExternalNav("http://127.0.0.1", "http://127.0.0.1:80/x") {
		t.Fatal("explicit default port should be the same origin")
	}
	if isExternalNav("http://127.0.0.1:80", "HTTP://127.0.0.1/x") {
		t.Fatal("implicit default port / upper-case scheme should be the same origin")
	}
	if isExternalNav("https://till.local", "https://TILL.local:443/x") {
		t.Fatal("explicit https default port / host case should be the same origin")
	}
}

func TestNavigationDisposition(t *testing.T) {
	const base = "http://127.0.0.1:8080"
	cases := []struct {
		name      string
		uri       string
		newWindow bool
		want      navDisposition
	}{
		{"till link, same view", "http://127.0.0.1:8080/sale", false, navDefault},
		{"external link, same view", "https://www.universaltill.com/download", false, navOpenExternal},
		{"external target=_blank", "https://www.universaltill.com/download", true, navOpenExternal},
		{"till target=_blank loads in this view", "http://127.0.0.1:8080/plugins", true, navLoadInView},
		{"other till target=_blank", "http://127.0.0.1:9090/plugins", true, navOpenExternal},
		{"blob download, same view", "blob:http://127.0.0.1:8080/abc", false, navDefault},
		{"about:blank new window left to WebKit", "about:blank", true, navDefault},
		{"empty new window left to WebKit", "", true, navDefault},
		{"garbage new window left to WebKit", "http://[::1", true, navDefault},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := navigationDisposition(base, tc.uri, tc.newWindow); got != tc.want {
				t.Fatalf("navigationDisposition(%q, %q, %v) = %v, want %v", base, tc.uri, tc.newWindow, got, tc.want)
			}
		})
	}
}
