package main

import "net/url"

// Pure policy behind the Linux shell's external-link routing (ut-docs#372)
// — untagged so plain `go test ./...` covers it; webkit_recovery_linux.go
// only wires it onto WebKitGTK's decide-policy signal. Without it a
// target="_blank" link (the update-download link, "open on the main till")
// did nothing, and a plain external link stranded the till on a page with
// no browser chrome and no way back. Same rule as webkit_darwin.go's
// UTDelegate: anything leaving the till opens in the system's default
// browser, the till itself stays in this view.

// navDisposition is what the shell does with one navigation request.
type navDisposition int

const (
	// navDefault: not ours to reroute — WebKit carries on as it would
	// without the handler.
	navDefault navDisposition = iota
	// navOpenExternal: refuse it in the view, hand it to the default browser.
	navOpenExternal
	// navLoadInView: a new-window request for the till's own origin —
	// refuse the popup and load it in this view instead (the shell has no
	// second window to put it in; same as webkit_darwin.go).
	navLoadInView
)

// isExternalNav reports whether targetURI leaves the till: an http(s) URI
// not on baseURL's origin (sameOrigin: scheme+host+port, default ports
// folded). Anything else — about:blank/about:srcdoc, data:, blob: (how
// the EOD and plugin exports download), javascript:, an empty or
// unparsable URI — is not external: there is nothing a browser should be
// handed, matching webkit_darwin.go's isExternalURL ("not ours to
// reroute"). Note sameOrigin alone would call all of those off-origin,
// which here would cancel blob: downloads.
func isExternalNav(baseURL, targetURI string) bool {
	u, err := url.Parse(targetURI)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return false
	}
	return !sameOrigin(baseURL, targetURI)
}

// navigationDisposition decides one WebKit navigation request for uri;
// newWindow is a window.open / target="_blank" request rather than a
// navigation of an existing frame.
func navigationDisposition(baseURL, uri string, newWindow bool) navDisposition {
	switch {
	case isExternalNav(baseURL, uri):
		return navOpenExternal
	case newWindow && sameOrigin(baseURL, uri):
		return navLoadInView
	}
	return navDefault
}
