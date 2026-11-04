package selfupdate

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/universaltill/universal-till/internal/buildinfo"
	"github.com/universaltill/universal-till/internal/logging"
	"github.com/universaltill/universal-till/internal/releasesig"
)

// ErrSignature marks a release refused because its checksums.txt signature
// is missing, malformed, by an unknown key or wrong (ADR-0178). The follow
// loop maps it to failed:signature.
var ErrSignature = errors.New("release signature check failed")

// sums maps an asset name to its lowercase SHA-256 from checksums.txt.
type sums map[string]string

func (s sums) lookup(name string) (string, error) {
	if h, ok := s[name]; ok {
		return h, nil
	}
	return "", fmt.Errorf("checksum not found for %s", name)
}

// httpStatusError is a release asset answered with a non-200 status.
type httpStatusError struct{ code int }

func (e *httpStatusError) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

// fetchAsset reads a release asset, capped at limit bytes.
func fetchAsset(ctx context.Context, url string, limit int64) ([]byte, error) {
	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	resp, err := outboundClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, &httpStatusError{resp.StatusCode}
	}
	return io.ReadAll(io.LimitReader(resp.Body, limit))
}

// verifiedChecksums is the one integrity root for every install path: it
// fetches the release's checksums.txt and, when this build carries release
// keys (buildinfo.ReleasePublicKeys), refuses it unless checksums.txt.sig is
// a valid signature over those exact bytes (ADR-0178). It runs before the
// artifact is downloaded. With no keys stamped it is dormant and only
// parses the file, as before.
func verifiedChecksums(ctx context.Context, rel *ghRelease) (sums, error) {
	log := logging.L()
	version := strings.TrimPrefix(rel.TagName, "v")
	sumsURL, sigURL := "", ""
	for _, a := range rel.Assets {
		switch a.Name {
		case "checksums.txt":
			sumsURL = a.URL
		case releasesig.SigAsset:
			sigURL = a.URL
		}
	}
	// Fail closed: every real release ships checksums.txt (.goreleaser.yaml
	// name_template), so a release without one only ever means a broken or
	// tampered release — never install unverified.
	if sumsURL == "" {
		return nil, fmt.Errorf("release v%s has no checksums.txt — refusing to install an unverified update", version)
	}
	body, err := fetchAsset(ctx, sumsURL, 1<<20)
	if err != nil {
		// Dormant behaviour is unchanged: a non-200 answer used to be parsed
		// as a body with no entry, i.e. "checksum not found" (failed:checksum).
		// Transport errors stay as they were (*url.Error, failed:network).
		var se *httpStatusError
		if errors.As(err, &se) {
			return nil, fmt.Errorf("checksum not found for v%s: checksums.txt answered %v", version, err)
		}
		return nil, err
	}

	if buildinfo.ReleasePublicKeys == "" {
		log.Infof("self-update: release metadata not signature-checked (no release key compiled in)")
		return parseSums(body), nil
	}
	keys, err := releasesig.ParseKeys(buildinfo.ReleasePublicKeys)
	if err != nil || len(keys) == 0 {
		if err == nil {
			err = errors.New("no keys")
		}
		log.Errorf("[selfupdate] compiled release key list is unusable: %v", err)
		return nil, fmt.Errorf("%w: compiled release key unusable: %v", ErrSignature, err)
	}
	if sigURL == "" {
		return nil, fmt.Errorf("%w: release v%s has no %s", ErrSignature, version, releasesig.SigAsset)
	}
	sig, err := fetchAsset(ctx, sigURL, 16<<10+1)
	if err != nil {
		// Only a missing asset (404) is evidence against the release. A
		// transport error or 5xx/other status is an outage, not tampering:
		// return it unwrapped so it takes the network/transient code path.
		var se *httpStatusError
		if errors.As(err, &se) {
			if se.code == http.StatusNotFound {
				return nil, fmt.Errorf("%w: release v%s has no %s (HTTP 404)", ErrSignature, version, releasesig.SigAsset)
			}
			return nil, fmt.Errorf("download release signature: %w", err)
		}
		return nil, err
	}
	id, err := releasesig.Verify(keys, body, sig)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrSignature, err)
	}
	log.Infof("[selfupdate] release v%s signature verified (key %s)", version, id)
	return parseSums(body), nil
}

func parseSums(body []byte) sums {
	s := sums{}
	for _, line := range strings.Split(string(body), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 {
			if _, dup := s[f[1]]; !dup {
				s[f[1]] = strings.ToLower(f[0])
			}
		}
	}
	return s
}
