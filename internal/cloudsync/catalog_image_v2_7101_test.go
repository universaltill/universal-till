package cloudsync

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func sha(c string) string { return strings.Repeat(c, 64) }

func imgs(items ...map[string]any) []any {
	out := make([]any, len(items))
	for i, m := range items {
		out[i] = m
	}
	return out
}

func decodeV2(t *testing.T, p map[string]any) (CatalogImage, string) {
	t.Helper()
	return decodeCatalogImage(payload(p))
}

// ut-docs#7101 (ADR-0218): set_catalog_image v2 carries images[]; the v1 sha256 is the primary.
func TestSetCatalogImageV2_DecodesGallery(t *testing.T) {
	img, bad := decodeV2(t, map[string]any{"entity": "item", "id": "i1", "sha256": sha("a"),
		"images": imgs(
			map[string]any{"sha256": sha("b"), "alt": "back", "position": 2.0},
			map[string]any{"sha256": sha("a"), "alt": "front", "position": 1.0})})
	if bad != "" {
		t.Fatal(bad)
	}
	if len(img.Images) != 2 || img.Images[0].SHA256 != sha("a") || img.Images[0].Alt != "front" || img.Images[1].SHA256 != sha("b") {
		t.Fatalf("images %+v (sorted by position)", img.Images)
	}
	if img.SHA256 != sha("a") || img.Clear {
		t.Fatalf("primary %q clear %v", img.SHA256, img.Clear)
	}
}

func TestSetCatalogImageV2_RefusedShapes(t *testing.T) {
	good := map[string]any{"sha256": sha("a"), "position": 1.0}
	ten := make([]any, 11)
	for i := range ten {
		ten[i] = map[string]any{"sha256": sha("a"), "position": float64(i + 1)}
	}
	for name, c := range map[string]struct {
		p    map[string]any
		want string
	}{
		"not array":     {map[string]any{"images": "x"}, "bad images"},
		"too many":      {map[string]any{"images": ten}, "too many images"},
		"bad sha":       {map[string]any{"images": imgs(map[string]any{"sha256": "zz", "position": 1.0})}, "bad image sha256"},
		"bad alt":       {map[string]any{"images": imgs(map[string]any{"sha256": sha("a"), "position": 1.0, "alt": 5.0})}, "bad image alt"},
		"alt too long":  {map[string]any{"images": imgs(map[string]any{"sha256": sha("a"), "position": 1.0, "alt": strings.Repeat("x", 513)})}, "image alt too long"},
		"gap":           {map[string]any{"images": imgs(good, map[string]any{"sha256": sha("b"), "position": 3.0})}, "image positions must be 1..n"},
		"duplicate pos": {map[string]any{"images": imgs(good, map[string]any{"sha256": sha("b"), "position": 1.0})}, "image positions must be 1..n"},
		"not object":    {map[string]any{"images": []any{"x"}}, "bad image"},
		"sha mismatch":  {map[string]any{"sha256": sha("c"), "images": imgs(good)}, "sha256 is not the first image"},
	} {
		p := map[string]any{"entity": "item", "id": "i1"}
		for k, v := range c.p {
			p[k] = v
		}
		if _, bad := decodeV2(t, p); bad != c.want {
			t.Errorf("%s: %q, want %q", name, bad, c.want)
		}
	}
}

func TestSetCatalogImageV2_EmptyListIsClear_V1Unchanged(t *testing.T) {
	img, bad := decodeV2(t, map[string]any{"entity": "item", "id": "i1", "images": []any{}})
	if bad != "" || !img.Clear || len(img.Images) != 0 {
		t.Fatalf("empty images: %+v %q", img, bad)
	}
	img, bad = decodeV2(t, map[string]any{"entity": "item", "id": "i1", "sha256": sha("a")})
	if bad != "" || img.Clear || img.Images != nil || img.SHA256 != sha("a") {
		t.Fatalf("v1: %+v %q", img, bad)
	}
}

func TestSetCatalogImageV2_VariantAndCategoryRules(t *testing.T) {
	img, bad := decodeV2(t, map[string]any{"entity": "item", "id": "i1", "variant_id": "v9", "images": imgs(map[string]any{"sha256": sha("a"), "position": 1.0})})
	if bad != "" || img.VariantID != "v9" {
		t.Fatalf("variant: %+v %q", img, bad)
	}
	if _, bad := decodeV2(t, map[string]any{"entity": "category", "id": "c1", "images": imgs(map[string]any{"sha256": sha("a"), "position": 1.0})}); bad != "images apply to items only" {
		t.Fatalf("category gallery: %q", bad)
	}
	if _, bad := decodeV2(t, map[string]any{"entity": "category", "id": "c1", "variant_id": "v", "sha256": sha("a")}); bad != "variant_id applies to items only" {
		t.Fatalf("category variant: %q", bad)
	}
}

// Each image is fetched with the same cloud route and checksum as v1.
func TestSetCatalogImageV2_FetchEachImage(t *testing.T) {
	var asked []string
	hooks := Hooks{
		fetchCatalogImage: func(_ context.Context, s string) ([]byte, error) { asked = append(asked, s); return []byte(s[:2]), nil },
		SetCatalogImage: func(ctx context.Context, img CatalogImage) (string, error) {
			for _, g := range img.Images {
				b, err := g.Fetch(ctx)
				if err != nil || string(b) != g.SHA256[:2] {
					return "", errors.New("fetch")
				}
			}
			return "gallery set", nil
		},
	}
	st, msg := apply(context.Background(), directive{Type: "set_catalog_image", Payload: map[string]any{"entity": "item", "id": "i1", "sha256": sha("a"),
		"images": imgs(map[string]any{"sha256": sha("a"), "position": 1.0}, map[string]any{"sha256": sha("b"), "position": 2.0})}}, hooks)
	if st != "applied" || msg != "gallery set" || len(asked) != 2 || asked[0] != sha("a") || asked[1] != sha("b") {
		t.Fatalf("%q %q asked %v", st, msg, asked)
	}
}
