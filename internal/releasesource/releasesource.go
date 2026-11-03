// Package releasesource turns the build-time release base
// (buildinfo.ReleasesBaseURL, ut-docs#4080) into the URLs the self-updater and
// the update check use, and decides which asset URLs a release may list.
//
// Derivation. The stamped value is the releases API collection without
// "/latest" (empty = DefaultBase, our own GitHub repo); "/latest" and
// "/tags/v<ver>" are appended here, so the check and the download agree.
//
// Trust. Assets must come from the host derived from the same value: for
// api.github.com/repos/O/R/releases that is
// https://github.com/O/R/releases/download/; for any other https host it is
// that same host and port. Hosts compare lower-cased without a trailing FQDN
// dot; a GitHub owner/repo path compares case-insensitively (GitHub's own
// rule). The check applies to the URL the release lists,
// not to the target of GitHub's 302 to objects.githubusercontent.com (the
// release-notes path works the same way); the SHA-256 against checksums.txt
// stays the integrity gate after the redirect.
//
// No environment, by design. This package never reads the environment, so a
// runtime override (UT_UPDATE_RELEASES_URL, which only redirects the check)
// can never move the download or its trusted host. A malformed stamped value
// is an error, never a fallback to DefaultBase: falling back would send an
// independent operator's tills to our releases.
package releasesource

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"path"
	"strings"

	"github.com/universaltill/universal-till/internal/buildinfo"
)

// DefaultBase is our own releases API collection, used when nothing is stamped.
const DefaultBase = "https://api.github.com/repos/universaltill/universal-till/releases"

var (
	// ErrInvalidBase means the value stamped into this build is unusable.
	ErrInvalidBase = errors.New("release source compiled into this build is invalid")
	// ErrUntrustedAsset means a release lists an asset off the trusted host.
	ErrUntrustedAsset = errors.New("release asset is not on the trusted release host")
)

// Source is a parsed release source. The zero value trusts nothing.
type Source struct {
	latestURL, tagPrefix       string // <base>/latest, <base>/tags/v
	assetHost, assetPathPrefix string // normHost form; path prefix
	foldPath                   bool   // compare the path case-insensitively (GitHub owner/repo)
}

// normHost is u's host[:port], lower-cased and without a trailing FQDN dot
// ("github.com." is github.com), so the host checks cannot be sidestepped by
// spelling (ut-docs#4080, tester F1).
func normHost(u *url.URL) (host, hostPort string) {
	host = strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	hostPort = host
	if strings.Contains(host, ":") {
		hostPort = "[" + host + "]"
	}
	if p := u.Port(); p != "" {
		hostPort = net.JoinHostPort(host, p)
	}
	return host, hostPort
}

// Build parses the value stamped into this binary.
func Build() (Source, error) { return Parse(buildinfo.ReleasesBaseURL) }

// Parse validates raw (see the package doc) and derives a Source.
func Parse(raw string) (Source, error) {
	bad := func(why string) (Source, error) { return Source{}, fmt.Errorf("%w: %s", ErrInvalidBase, why) }
	raw = strings.TrimSpace(raw)
	if raw == "" {
		raw = DefaultBase
	}
	raw = strings.TrimSuffix(raw, "/")
	raw = strings.TrimSuffix(raw, "/latest")
	u, ok := CleanURL(raw)
	if !ok {
		return bad("not a plain absolute URL")
	}
	if u.Scheme != "https" {
		return bad("must be an https URL")
	}
	host, hostPort := normHost(u)
	if host == "api.github.com" {
		parts := strings.Split(strings.TrimPrefix(u.Path, "/"), "/")
		if u.Port() != "" || len(parts) != 4 || parts[0] != "repos" || parts[1] == "" || parts[2] == "" || parts[3] != "releases" {
			return bad("GitHub base must be https://api.github.com/repos/<owner>/<repo>/releases")
		}
		base := "https://api.github.com" + u.Path
		return Source{
			latestURL:       base + "/latest",
			tagPrefix:       base + "/tags/v",
			assetHost:       "github.com",
			assetPathPrefix: strings.ToLower("/" + parts[1] + "/" + parts[2] + "/releases/download/"),
			foldPath:        true,
		}, nil
	}
	if host == "github.com" || strings.HasSuffix(host, ".github.com") || strings.HasSuffix(host, ".githubusercontent.com") {
		return bad("GitHub hosts other than api.github.com are not release APIs")
	}
	base := "https://" + hostPort + u.Path
	return Source{
		latestURL:       base + "/latest",
		tagPrefix:       base + "/tags/v",
		assetHost:       hostPort,
		assetPathPrefix: "/",
	}, nil
}

// LatestURL is where the newest release's metadata lives.
func (s Source) LatestURL() string { return s.latestURL }

// TagURL is where release `version` (without the "v") lives; the caller
// validates version.
func (s Source) TagURL(version string) string { return s.tagPrefix + version }

// AssetTrusted reports whether raw is an https asset URL on the trusted host.
// A zero Source trusts nothing.
func (s Source) AssetTrusted(raw string) bool {
	if s.assetHost == "" {
		return false
	}
	u, ok := CleanURL(raw)
	if !ok || u.Scheme != "https" {
		return false
	}
	p := u.Path
	if s.foldPath {
		p = strings.ToLower(p)
	}
	_, hostPort := normHost(u)
	return hostPort == s.assetHost && strings.HasPrefix(p, s.assetPathPrefix)
}

// CleanURL parses raw and applies the shared URL hygiene: no userinfo, query
// (not even an empty "?") or fragment, a host, and no dot segments however
// encoded. It does not check the scheme.
func CleanURL(raw string) (*url.URL, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || u.Host == "" {
		return nil, false
	}
	if strings.Contains(u.Path, "..") || (u.Path != "" && path.Clean(u.Path) != u.Path) {
		return nil, false
	}
	return u, true
}
