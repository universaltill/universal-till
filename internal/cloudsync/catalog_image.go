package cloudsync

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/universaltill/universal-till/internal/config"
	"github.com/universaltill/universal-till/internal/enroll"
	"github.com/universaltill/universal-till/internal/netaccess"
	"github.com/universaltill/universal-till/internal/paths"
)

// set_catalog_image (ut-docs reference/manage-shop-catalog-api.md §3.9,
// ut-docs#3076/#3139): an item or category image the owner set (or
// removed) in my. The directive carries only the image's content address;
// the bytes come from the cloud's till fetch route (§2.13) with the
// store's credential.
//
// Split: the decode and the fetch live here, because the fetch is HTTP to
// the cloud with this package's endpoint/credential and client (the same
// ones post() uses). The hook (pages.cloudSetCatalogImage) owns everything
// that touches the till's own data: the "is this id on this till" check,
// the thumbnail write through the till's upload path, the row, the audit.
// The hook calls CatalogImage.Fetch only after its id check, so a
// directive for an unknown item downloads nothing.

// catalogImageMaxBytes caps a fetched image (§3.9 rule 2): the cloud
// stores at most a 1600px PNG made from a ≤10 MiB upload; 11 MiB leaves
// headroom and still bounds a misbehaving response.
const catalogImageMaxBytes = 11 << 20

// catalogImageFetchTimeout bounds one fetch, so a stalled cloud costs the
// tick at most this much (a var so a test can shorten it). Directive
// application already runs on the sync loop's goroutine, off the sale path.
var catalogImageFetchTimeout = 60 * time.Second

// catalogImageClient shares httpClient's pooled transport but not its 30 s
// whole-request timeout: an 11 MiB body on a slow shop line may need longer,
// and catalogImageFetchTimeout's context bounds it instead.
var catalogImageClient = netaccess.NewClientWithTransport(0, netaccess.BaseTransport(httpClient.Transport))

var sha256Hex = regexp.MustCompile(`^[0-9a-f]{64}$`)

// errImageFetchUnreachable marks a transport-level fetch failure (connect
// error, timeout, a body cut off mid-read) as opposed to an answer from the
// cloud (non-200, oversize, checksum). tick leaves such a directive — and
// every later set_catalog_image in the same tick — pending instead of
// failing it, so a stalled image route costs one tick at most one
// catalogImageFetchTimeout and the next tick retries (review 2026-09-30,
// ut-docs#3139).
var errImageFetchUnreachable = errors.New("could not reach the cloud to download the image")

// CatalogImage is one decoded set_catalog_image directive. Entity is
// "item" or "category"; exactly one of SHA256 (set) and Clear holds.
type CatalogImage struct {
	Entity string
	ID     string
	SHA256 string
	// Size is the cloud's byte count for the PNG (0 when absent); the
	// checksum, not this, is what a fetched body is checked against.
	Size  int64
	Clear bool
	// Images is the v2 gallery (ut-docs#7101, ADR-0218), in position order
	// (index 0 = the primary, equal to SHA256); nil for a v1 directive or a
	// clear. VariantID, when set, addresses one variant's gallery of Entity item.
	Images    []CatalogImageRef
	VariantID string
	// Fetch downloads the PNG from the cloud and checks it hashes to
	// SHA256 (nil when Clear). Every error is an owner-readable sentence.
	Fetch func(ctx context.Context) ([]byte, error)
}

// CatalogImageRef is one picture of a v2 gallery. Fetch downloads and
// checksums it like CatalogImage.Fetch.
type CatalogImageRef struct {
	SHA256 string
	Alt    string
	Fetch  func(ctx context.Context) ([]byte, error)
}

// maxGalleryImages and maxGalleryAlt mirror the cloud's limits (ADR-0218).
const (
	maxGalleryImages = 10
	maxGalleryAlt    = 512
)

// decodeGallery reads the optional v2 "images" list: present = a gallery
// (an empty list clears), positions must be exactly 1..n, returned in order.
func decodeGallery(p payload) (imgs []CatalogImageRef, present bool, msg string) {
	v, ok := p["images"]
	if !ok || v == nil {
		return nil, false, ""
	}
	list, ok := v.([]any)
	if !ok {
		return nil, true, "bad images"
	}
	if len(list) > maxGalleryImages {
		return nil, true, "too many images"
	}
	ordered := make([]CatalogImageRef, len(list))
	seen := make([]bool, len(list))
	for _, e := range list {
		m, ok := e.(map[string]any)
		if !ok {
			return nil, true, "bad image"
		}
		ip := payload(m)
		sum, ok := ip.optStr("sha256")
		if !ok || sum == nil || !sha256Hex.MatchString(*sum) {
			return nil, true, "bad image sha256"
		}
		alt, ok := ip.optStr("alt")
		if !ok {
			return nil, true, "bad image alt"
		}
		pos, ok := ip.optInt("position")
		if !ok || pos == nil || *pos < 1 || int(*pos) > len(list) || seen[*pos-1] {
			return nil, true, "image positions must be 1..n"
		}
		r := CatalogImageRef{SHA256: *sum}
		if alt != nil {
			if len(*alt) > maxGalleryAlt {
				return nil, true, "image alt too long"
			}
			r.Alt = *alt
		}
		seen[*pos-1] = true
		ordered[*pos-1] = r
	}
	return ordered, true, ""
}

// decodeCatalogImage reads a set_catalog_image payload (§3.9 rule 1). A
// present field of the wrong shape fails; a non-empty msg is the failure.
func decodeCatalogImage(p payload) (CatalogImage, string) {
	var out CatalogImage
	entity, ok := p.optStr("entity")
	switch {
	case !ok:
		return out, "bad entity"
	case entity == nil || *entity == "":
		return out, "missing entity"
	case *entity != "item" && *entity != "category":
		return out, "unknown entity " + *entity
	}
	out.Entity = *entity
	id, ok := p.optStr("id")
	if !ok {
		return out, "bad id"
	}
	if id == nil || *id == "" {
		return out, "missing id"
	}
	out.ID = *id
	sum, ok := p.optStr("sha256")
	if !ok || (sum != nil && *sum != "" && !sha256Hex.MatchString(*sum)) {
		return out, "bad sha256"
	}
	clear, ok := p.optBool("clear")
	if !ok {
		return out, "bad clear"
	}
	out.Clear = clear != nil && *clear
	if sum != nil {
		out.SHA256 = *sum
	}
	if vid, ok := p.optStr("variant_id"); !ok {
		return out, "bad variant_id"
	} else if vid != nil && strings.TrimSpace(*vid) != "" {
		if out.Entity != "item" {
			return out, "variant_id applies to items only"
		}
		out.VariantID = strings.TrimSpace(*vid)
	}
	gallery, hasGallery, msg := decodeGallery(p)
	if msg != "" {
		return out, msg
	}
	if hasGallery {
		if out.Entity != "item" {
			return out, "images apply to items only"
		}
		switch {
		case out.Clear && len(gallery) > 0:
			return out, "images and clear cannot both be set"
		case len(gallery) == 0:
			out.Clear, out.SHA256 = true, "" // an empty gallery removes the pictures
		case out.SHA256 != "" && out.SHA256 != gallery[0].SHA256:
			return out, "sha256 is not the first image"
		default:
			out.SHA256, out.Images = gallery[0].SHA256, gallery
		}
	}
	size, ok := p.optInt("size")
	if !ok || (size != nil && (*size < 0 || *size > catalogImageMaxBytes)) {
		return out, "bad size"
	}
	if size != nil {
		out.Size = *size
	}
	switch {
	case out.Clear && out.SHA256 != "":
		return out, "sha256 and clear cannot both be set"
	case !out.Clear && out.SHA256 == "":
		return out, "missing sha256"
	}
	return out, ""
}

// fetchCatalogImage downloads GET <endpoint>/v1/stores/catalog-images/{sha}
// (the endpoint base already ends in /api) with the till's store Bearer
// credential, capped at catalogImageMaxBytes, and checks the body's
// SHA-256. A failure says what went wrong and that saving the image again
// in my. retries it: the cloud does not re-send a failed directive itself.
func fetchCatalogImage(ctx context.Context, cfg *config.Config, sum string) ([]byte, error) {
	m := enroll.Effective(cfg).Marketplace
	if !enroll.CredentialsComplete(m) {
		return nil, errors.New("this till is not connected to the cloud")
	}
	if !sha256Hex.MatchString(sum) {
		return nil, errors.New("bad sha256")
	}
	ctx, cancel := context.WithTimeout(ctx, catalogImageFetchTimeout)
	defer cancel()
	url := strings.TrimRight(m.EndpointURL, "/") + "/v1/stores/catalog-images/" + sum
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+m.MerchantToken)
	resp, err := catalogImageClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", errImageFetchUnreachable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		drainBody(resp)
		return nil, fmt.Errorf("could not download the image from the cloud (status %d); save the image again to retry", resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, catalogImageMaxBytes+1))
	if err != nil {
		return nil, fmt.Errorf("%w (%v)", errImageFetchUnreachable, err)
	}
	if len(body) > catalogImageMaxBytes {
		return nil, fmt.Errorf("the image from the cloud is too large (over %d MiB)", catalogImageMaxBytes>>20)
	}
	got := sha256.Sum256(body)
	if hex.EncodeToString(got[:]) != sum {
		return nil, errors.New("image checksum mismatch")
	}
	return body, nil
}

// servedImageRoots are the uploaded-photo trees under /public/assets/ whose
// files the till serves from its stable data dir (paths.Data): an item's
// or category's thumb.png. Any other image path (a built-in icon, a
// library tile) is not a photo and reports "".
var servedImageRoots = []string{"items", "categories"}

type servedImageEntry struct {
	size int64
	mod  time.Time
	sum  string
}

// servedImageCache memoises ServedImageSHA256 by file path, keyed on size
// and modification time: every tick builds the snapshot and the config
// report, and re-hashing every photo each time would read the whole photo
// set from disk every two minutes. A rewritten file (the upload and the
// directive both rename a new file into place) gets a new mtime.
var servedImageCache sync.Map // abs path -> servedImageEntry

// ServedImageSHA256 is the hex SHA-256 of the PNG the till serves for an
// item/category image path (item_images.path, categories.image_path), or ""
// when the path is empty, not an uploaded photo (a built-in icon), or its
// file is missing (§3.9 rule 5).
func ServedImageSHA256(publicPath string) string {
	rel, ok := strings.CutPrefix(publicPath, "/public/assets/")
	if !ok {
		return ""
	}
	parts := strings.Split(rel, "/")
	if len(parts) < 2 {
		return ""
	}
	known := false
	for _, r := range servedImageRoots {
		known = known || parts[0] == r
	}
	for _, p := range parts {
		if p == "" || p == "." || p == ".." || strings.Contains(p, `\`) {
			return ""
		}
	}
	if !known {
		return ""
	}
	file := paths.Data(append([]string{"public", "assets"}, parts...)...)
	root := paths.Data("public", "assets", parts[0]) + string(filepath.Separator)
	if !strings.HasPrefix(filepath.Clean(file), root) {
		return ""
	}
	f, err := os.Open(file)
	if err != nil {
		servedImageCache.Delete(file)
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || !fi.Mode().IsRegular() {
		return ""
	}
	if v, ok := servedImageCache.Load(file); ok {
		e := v.(servedImageEntry)
		if e.size == fi.Size() && e.mod.Equal(fi.ModTime()) {
			return e.sum
		}
	}
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return ""
	}
	sum := hex.EncodeToString(h.Sum(nil))
	servedImageCache.Store(file, servedImageEntry{size: fi.Size(), mod: fi.ModTime(), sum: sum})
	return sum
}
