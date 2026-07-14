// Package lookup resolves retail barcodes against open product databases
// (Open Food Facts family) so the catalog page can pre-fill new items.
// Back-office convenience only — nothing at checkout depends on it.
package lookup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ErrNotFound means every source answered but none knows the barcode.
var ErrNotFound = errors.New("barcode not found in any product database")

const userAgent = "UniversalTill/0.1 (https://universaltill.com)"

// Product is what a successful lookup yields for form pre-fill.
type Product struct {
	Barcode     string `json:"barcode"`
	Name        string `json:"name"`
	Brand       string `json:"brand"`
	Quantity    string `json:"quantity"`
	Description string `json:"description"`
	ImageURL    string `json:"image_url"`
	Source      string `json:"source"`
}

// Source is one product database speaking the Open Food Facts v2 API shape.
type Source struct {
	Name    string
	BaseURL string
}

// DefaultSources covers general food, non-food and cosmetics barcodes.
func DefaultSources() []Source {
	return []Source{
		{Name: "Open Food Facts", BaseURL: "https://world.openfoodfacts.org"},
		{Name: "Open Products Facts", BaseURL: "https://world.openproductsfacts.org"},
		{Name: "Open Beauty Facts", BaseURL: "https://world.openbeautyfacts.org"},
	}
}

// imageHostSuffixes is the allowlist for FetchImage — only images hosted by
// the lookup sources themselves may be downloaded (SSRF guard).
var imageHostSuffixes = []string{
	".openfoodfacts.org", ".openproductsfacts.org", ".openbeautyfacts.org",
}

type Client struct {
	http    *http.Client
	sources []Source
}

// NewClient builds a lookup client; nil arguments select the defaults.
func NewClient(hc *http.Client, sources []Source) *Client {
	if hc == nil {
		hc = &http.Client{Timeout: 6 * time.Second}
	}
	if len(sources) == 0 {
		sources = DefaultSources()
	}
	return &Client{http: hc, sources: sources}
}

// ValidBarcode accepts 6–14 digits: EAN-8, UPC-A, EAN-13, GTIN-14 and the
// short internal codes some shops print themselves.
func ValidBarcode(code string) bool {
	if len(code) < 6 || len(code) > 14 {
		return false
	}
	for _, r := range code {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

type offResponse struct {
	Status  int `json:"status"`
	Product struct {
		ProductName string `json:"product_name"`
		GenericName string `json:"generic_name"`
		Brands      string `json:"brands"`
		Quantity    string `json:"quantity"`
		ImageURL    string `json:"image_front_url"`
	} `json:"product"`
}

// Lookup tries each source in order and returns the first hit.
// ErrNotFound only when every source answered negatively; any transport
// failure on all sources surfaces as a joined error instead.
func (c *Client) Lookup(ctx context.Context, barcode string) (Product, error) {
	if !ValidBarcode(barcode) {
		return Product{}, fmt.Errorf("invalid barcode %q", barcode)
	}
	var transportErrs []error
	answered := false
	for _, s := range c.sources {
		p, err := c.lookupOne(ctx, s, barcode)
		switch {
		case err == nil:
			return p, nil
		case errors.Is(err, ErrNotFound):
			answered = true
		default:
			transportErrs = append(transportErrs, fmt.Errorf("%s: %w", s.Name, err))
		}
	}
	if answered {
		return Product{}, ErrNotFound
	}
	return Product{}, errors.Join(transportErrs...)
}

func (c *Client) lookupOne(ctx context.Context, s Source, barcode string) (Product, error) {
	u := s.BaseURL + "/api/v2/product/" + url.PathEscape(barcode) +
		"?fields=product_name,generic_name,brands,quantity,image_front_url"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return Product{}, err
	}
	req.Header.Set("User-Agent", userAgent)
	res, err := c.http.Do(req)
	if err != nil {
		return Product{}, err
	}
	defer res.Body.Close()
	if res.StatusCode == http.StatusNotFound {
		return Product{}, ErrNotFound
	}
	if res.StatusCode != http.StatusOK {
		return Product{}, fmt.Errorf("unexpected status %d", res.StatusCode)
	}
	var body offResponse
	if err := json.NewDecoder(io.LimitReader(res.Body, 1<<20)).Decode(&body); err != nil {
		return Product{}, err
	}
	name := strings.TrimSpace(body.Product.ProductName)
	if body.Status != 1 || name == "" {
		return Product{}, ErrNotFound
	}
	return Product{
		Barcode:     barcode,
		Name:        name,
		Brand:       strings.TrimSpace(body.Product.Brands),
		Quantity:    strings.TrimSpace(body.Product.Quantity),
		Description: strings.TrimSpace(body.Product.GenericName),
		ImageURL:    strings.TrimSpace(body.Product.ImageURL),
		Source:      s.Name,
	}, nil
}

// FetchImage downloads a product image for thumbnail use. The URL host must
// belong to one of the lookup sources' image domains; anything else is
// refused so the endpoint can never be steered at internal services.
func (c *Client) FetchImage(ctx context.Context, imageURL string) ([]byte, error) {
	u, err := url.Parse(imageURL)
	if err != nil || u.Scheme != "https" {
		return nil, errors.New("image url must be https")
	}
	allowed := false
	for _, suffix := range imageHostSuffixes {
		if strings.HasSuffix(u.Hostname(), suffix) {
			allowed = true
			break
		}
	}
	if !allowed {
		return nil, fmt.Errorf("image host %q not allowed", u.Hostname())
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	res, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status %d", res.StatusCode)
	}
	return io.ReadAll(io.LimitReader(res.Body, 5<<20))
}
