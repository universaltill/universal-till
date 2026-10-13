package pages

import (
	"io"
	"net/http"
	"net/http/httptest"
)

// localRequest is httptest.NewRequest from the till's own machine. A test
// driving the full Init chain needs it: httptest's default peer, 192.0.2.1,
// is a LAN caller to the ADR-0157 guard (internal/languard) and gets 403.
func localRequest(method, target string, body io.Reader) *http.Request {
	r := httptest.NewRequest(method, target, body)
	r.RemoteAddr = "127.0.0.1:40000"
	return r
}
