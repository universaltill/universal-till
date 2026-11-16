package photoimport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"syscall"
	"time"

	"github.com/universaltill/universal-till/internal/netaccess"
)

// ErrBlocked: the URL or the address it resolves to is not allowed.
var ErrBlocked = errors.New("photoimport: address not allowed")

// ErrFetch: the download failed (status, network, timeout).
var ErrFetch = errors.New("photoimport: download failed")

const (
	maxRedirects = 3
	fetchTimeout = 20 * time.Second
)

// BlockedIP reports whether ip is an address a till must never fetch from:
// loopback, private, link-local (covers the cloud metadata address),
// carrier-grade NAT, unspecified, multicast, IPv4-mapped forms of those, and
// IPv6 forms that tunnel to an IPv4 address (NAT64 and 6to4 are judged by
// the embedded address; Teredo is refused outright).
func BlockedIP(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.Is6() {
		b := ip.As16()
		switch {
		case nat64.Contains(ip):
			return BlockedIP(netip.AddrFrom4([4]byte{b[12], b[13], b[14], b[15]}))
		case sixToFour.Contains(ip):
			return BlockedIP(netip.AddrFrom4([4]byte{b[2], b[3], b[4], b[5]}))
		case teredo.Contains(ip), nat64Local.Contains(ip):
			return true
		}
	}
	if !ip.IsValid() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsMulticast() || ip.IsUnspecified() || ip.IsInterfaceLocalMulticast() {
		return true
	}
	if ip.Is4() {
		b := ip.As4()
		if b[0] == 100 && b[1]&0xC0 == 64 { // 100.64.0.0/10
			return true
		}
		if b[0] == 0 || b[0] >= 240 || (b[0] == 192 && b[1] == 0 && b[2] == 0) || (b[0] == 198 && (b[1] == 18 || b[1] == 19)) {
			return true
		}
	}
	return false
}

var (
	nat64      = netip.MustParsePrefix("64:ff9b::/96")
	nat64Local = netip.MustParsePrefix("64:ff9b:1::/48")
	sixToFour  = netip.MustParsePrefix("2002::/16")
	teredo     = netip.MustParsePrefix("2001::/32")
)

// Fetcher downloads https images. The address check runs in the dialer's
// Control hook on the address actually being connected to, so redirects and
// DNS rebinding are covered (a name that resolves to a private address at
// connect time is refused, whatever it resolved to earlier).
type Fetcher struct {
	client *http.Client
}

// NewFetcher builds the production fetcher.
func NewFetcher() *Fetcher { return newFetcher(BlockedIP, nil) }

// newFetcher lets a test allow its loopback server and trust its CA.
func newFetcher(blocked func(netip.Addr) bool, tlsCfg *tls.Config) *Fetcher {
	dialer := &net.Dialer{ // netaccess:allow wrapped by netaccess.NewClientWithTransport below (demo deny); Control must see the dialled address
		Timeout: 10 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			ap, err := netip.ParseAddrPort(address)
			if err != nil || blocked(ap.Addr()) {
				return ErrBlocked
			}
			return nil
		},
	}
	tr := &http.Transport{
		Proxy:                  nil, // never an environment proxy: it would dial for us unchecked
		DialContext:            dialer.DialContext,
		TLSClientConfig:        tlsCfg,
		TLSHandshakeTimeout:    10 * time.Second,
		ResponseHeaderTimeout:  15 * time.Second,
		DisableKeepAlives:      true,
		MaxResponseHeaderBytes: 64 << 10,
	}
	client := netaccess.NewClientWithTransport(fetchTimeout, tr)
	client.CheckRedirect = func(req *http.Request, via []*http.Request) error {
		if len(via) > maxRedirects {
			return ErrFetch
		}
		return checkURL(req.URL)
	}
	return &Fetcher{client: client}
}

func checkURL(u *url.URL) error {
	if u.Scheme != "https" || u.Host == "" || u.User != nil {
		return ErrBlocked
	}
	if p := u.Port(); p != "" && p != "443" {
		return ErrBlocked
	}
	if ip, err := netip.ParseAddr(u.Hostname()); err == nil && BlockedIP(ip) {
		return ErrBlocked
	}
	return nil
}

// Fetch downloads rawURL, at most max bytes.
func (f *Fetcher) Fetch(ctx context.Context, rawURL string, max int64) ([]byte, error) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil, ErrBlocked
	}
	if err := checkURL(u); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, ErrBlocked
	}
	req.Header.Set("User-Agent", "UniversalTill-photo-import")
	resp, err := f.client.Do(req)
	if err != nil {
		if errors.Is(err, ErrBlocked) {
			return nil, ErrBlocked
		}
		return nil, fmt.Errorf("%w", ErrFetch)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, ErrFetch
	}
	if resp.ContentLength > max {
		return nil, ErrTooBig
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, max+1))
	if err != nil {
		return nil, ErrFetch
	}
	if int64(len(b)) > max {
		return nil, ErrTooBig
	}
	return b, nil
}
