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
	// Fetch downloads the PNG from the cloud and checks it hashes to
	// SHA256 (nil when Clear). Every error is an owner-readable sentence.
	Fetch func(ctx context.Context) ([]byte, error)
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
