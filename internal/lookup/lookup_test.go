package lookup

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestValidBarcode(t *testing.T) {
	valid := []string{"123456", "40084107", "5449000000996", "12345678901234"}
	for _, c := range valid {
		if !ValidBarcode(c) {
			t.Errorf("ValidBarcode(%q) = false, want true", c)
		}
	}
	invalid := []string{"", "12345", "123456789012345", "50note96", "5449-000-0996", " 5449000000996"}
	for _, c := range invalid {
		if ValidBarcode(c) {
			t.Errorf("ValidBarcode(%q) = true, want false", c)
		}
	}
}

func newTestSource(t *testing.T, handler http.HandlerFunc) (Source, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return Source{Name: "test", BaseURL: srv.URL}, srv
}

func TestLookupHit(t *testing.T) {
	src, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v2/product/5449000000996" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("missing custom User-Agent")
		}
		_, _ = w.Write([]byte(`{"status":1,"product":{"product_name":"Coca-Cola","generic_name":"Cola soft drink","brands":"Coca-Cola","quantity":"330 ml","image_front_url":"https://images.openfoodfacts.org/x/front.jpg"}}`))
	})
	c := NewClient(nil, []Source{src})
	p, err := c.Lookup(context.Background(), "5449000000996")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if p.Name != "Coca-Cola" || p.Quantity != "330 ml" || p.Description != "Cola soft drink" || p.Source != "test" {
		t.Errorf("unexpected product: %+v", p)
	}
}

func TestLookupFallsThroughSources(t *testing.T) {
	miss, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	hit, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":1,"product":{"product_name":"Widget"}}`))
	})
	c := NewClient(nil, []Source{miss, hit})
	p, err := c.Lookup(context.Background(), "40084107")
	if err != nil {
		t.Fatalf("Lookup: %v", err)
	}
	if p.Name != "Widget" {
		t.Errorf("got %+v", p)
	}
}

func TestLookupNotFoundEverywhere(t *testing.T) {
	// status 0 body (OFF answers 200 or 404 for unknown codes depending on age).
	miss, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":0,"product":{}}`))
	})
	c := NewClient(nil, []Source{miss})
	if _, err := c.Lookup(context.Background(), "40084107"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestLookupTransportErrorIsNotNotFound(t *testing.T) {
	src, srv := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {})
	srv.Close() // connection refused from here on
	c := NewClient(nil, []Source{src})
	_, err := c.Lookup(context.Background(), "40084107")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("want transport error, got %v", err)
	}
}

func TestLookupRejectsInvalidBarcode(t *testing.T) {
	c := NewClient(nil, nil)
	if _, err := c.Lookup(context.Background(), "drop table"); err == nil {
		t.Fatal("want error for invalid barcode")
	}
}

// Each case must be refused BY THE ALLOWLIST ITSELF — asserting only
// err != nil would still pass if the guard were deleted entirely (the
// request would then hit a real host and fail with a network error
// instead, silently defeating the SSRF check this test exists to prove).
func TestFetchImageAllowlist(t *testing.T) {
	c := NewClient(nil, nil)
	cases := []struct {
		url        string
		wantReason string
	}{
		{"http://images.openfoodfacts.org/a.jpg", "https"},             // not https
		{"https://evil.example.com/a.jpg", "not allowed"},              // wrong host
		{"https://openfoodfacts.org.evil.com/a.jpg", "not allowed"},    // suffix spoof
		{"https://localhost/a.jpg", "not allowed"},                     // internal
		{"https://images.openfoodfacts.org@evil.com/x", "not allowed"}, // userinfo trick
	}
	for _, tc := range cases {
		_, err := c.FetchImage(context.Background(), tc.url)
		if err == nil {
			t.Errorf("FetchImage(%q) succeeded, want refusal", tc.url)
			continue
		}
		if !strings.Contains(err.Error(), tc.wantReason) {
			t.Errorf("FetchImage(%q) error = %q, want it to contain %q (i.e. refused by the allowlist, not some other failure)", tc.url, err.Error(), tc.wantReason)
		}
	}
}

// rewriteTransport lets a test present an allowlisted hostname to
// FetchImage's URL-validation step while the actual bytes go to a local
// httptest.Server — the allowlist check runs on url.Parse(imageURL), not on
// the transport, so this exercises the real request/response path without
// needing a TLS cert for a fake openfoodfacts.org.
type rewriteTransport struct{ target string }

func (rt *rewriteTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req = req.Clone(req.Context())
	req.URL.Scheme = "http"
	req.URL.Host = rt.target
	return http.DefaultTransport.RoundTrip(req)
}

func TestFetchImageSuccess(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != userAgent {
			t.Errorf("missing custom User-Agent")
		}
		_, _ = w.Write([]byte("fake-jpeg-bytes"))
	}))
	defer srv.Close()
	c := NewClient(&http.Client{Transport: &rewriteTransport{target: strings.TrimPrefix(srv.URL, "http://")}}, nil)

	body, err := c.FetchImage(context.Background(), "https://images.openfoodfacts.org/front.jpg")
	if err != nil {
		t.Fatalf("FetchImage: %v", err)
	}
	if string(body) != "fake-jpeg-bytes" {
		t.Errorf("body = %q", body)
	}
}

func TestFetchImageNon200(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := NewClient(&http.Client{Transport: &rewriteTransport{target: strings.TrimPrefix(srv.URL, "http://")}}, nil)

	if _, err := c.FetchImage(context.Background(), "https://images.openfoodfacts.org/gone.jpg"); err == nil {
		t.Fatal("want an error for a non-200 image response")
	}
}

func TestFetchImageTransportError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := strings.TrimPrefix(srv.URL, "http://")
	srv.Close() // connection refused from here on
	c := NewClient(&http.Client{Transport: &rewriteTransport{target: addr}}, nil)

	if _, err := c.FetchImage(context.Background(), "https://images.openfoodfacts.org/gone.jpg"); err == nil {
		t.Fatal("want a transport error when the image host is unreachable")
	}
}

// lookupOne's own request-construction error path: a source whose BaseURL
// carries a control character fails at http.NewRequestWithContext before
// any network call — distinct from ErrNotFound/transport-error handling.
func TestLookupMalformedSourceURL(t *testing.T) {
	c := NewClient(nil, []Source{{Name: "bad", BaseURL: "http://example.com\n"}})
	_, err := c.Lookup(context.Background(), "40084107")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("want a request-construction error, got %v", err)
	}
}

// A source answering with an unexpected non-404 status (e.g. its own 500)
// must surface as a transport-class error, not be treated as ErrNotFound.
func TestLookupUnexpectedStatus(t *testing.T) {
	src, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	c := NewClient(nil, []Source{src})
	_, err := c.Lookup(context.Background(), "40084107")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("want a transport-class error for HTTP 500, got %v", err)
	}
}

// A source answering 200 with unparseable JSON must surface a decode error,
// not silently fall through as ErrNotFound.
func TestLookupDecodeError(t *testing.T) {
	src, _ := newTestSource(t, func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{not json`))
	})
	c := NewClient(nil, []Source{src})
	_, err := c.Lookup(context.Background(), "40084107")
	if err == nil || errors.Is(err, ErrNotFound) {
		t.Fatalf("want a decode error, got %v", err)
	}
}
