package lookup

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
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

func TestFetchImageAllowlist(t *testing.T) {
	c := NewClient(nil, nil)
	bad := []string{
		"http://images.openfoodfacts.org/a.jpg",       // not https
		"https://evil.example.com/a.jpg",              // wrong host
		"https://openfoodfacts.org.evil.com/a.jpg",    // suffix spoof
		"https://localhost/a.jpg",                     // internal
		"https://images.openfoodfacts.org@evil.com/x", // userinfo trick
	}
	for _, u := range bad {
		if _, err := c.FetchImage(context.Background(), u); err == nil {
			t.Errorf("FetchImage(%q) succeeded, want refusal", u)
		}
	}
}
