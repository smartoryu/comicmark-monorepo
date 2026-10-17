// Package scraper fetches comic chapter pages and extracts their metadata.
//
// Fetching a URL that the user supplied makes this package the one place in
// ComicMark that can be pointed at arbitrary network destinations, so it is
// deliberately defensive: the destination is classified before the request, the
// address actually connected to is re-checked, and every redirect hop is
// validated again. See Guard.
package scraper

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"
	"syscall"
	"time"
)

// Errors returned when a URL is refused. They are distinct so the API can answer
// "that link is not allowed" rather than a generic failure.
var (
	// ErrURLNotAllowed means the URL is malformed, uses a scheme we refuse, or
	// resolves to an address that must not be fetched.
	ErrURLNotAllowed = errors.New("scraper: url is not allowed")

	// ErrResolutionFailed means the host could not be resolved. It is separate
	// because a temporary DNS failure is not the user's mistake.
	ErrResolutionFailed = errors.New("scraper: could not resolve host")
)

// maxRedirects bounds a redirect chain. Each hop is re-validated.
const maxRedirects = 5

// allowedSchemes is the only set of schemes worth fetching. Everything else
// (file, ftp, gopher, data, javascript) either is not HTTP or has no business
// being fetched by a server on a user's behalf.
var allowedSchemes = map[string]bool{
	"http":  true,
	"https": true,
}

// standardPorts do not need to be spelled out in a URL. Anything else is refused
// unless private addresses are allowed, which is how the development fixture on
// an arbitrary port works.
var standardPorts = map[string]bool{
	"":    true,
	"80":  true,
	"443": true,
}

// Guard decides which URLs may be fetched.
//
// It is used in three places, and all three matter:
//   - before the request, so an obviously bad destination never gets dialled
//   - inside the dialer, against the address actually being connected to, which
//     is what closes the DNS-rebinding gap a resolve-then-fetch check leaves
//   - on every redirect, because a permitted host can redirect anywhere
type Guard struct {
	// allowPrivate disables the address classification. It exists for local
	// development and the end-to-end script, which scrape a fixture served on
	// 127.0.0.1. Configuration refuses to allow it in production.
	allowPrivate bool

	// lookupIP resolves a host. It is injectable so the tests do not depend on
	// DNS, and so a resolver with different behaviour could be substituted.
	lookupIP func(ctx context.Context, host string) ([]net.IP, error)

	// timeout bounds the resolution step.
	timeout time.Duration
}

// GuardOptions configures a Guard.
type GuardOptions struct {
	// AllowPrivate disables SSRF protection. Development only.
	AllowPrivate bool
	// LookupIP overrides host resolution. Defaults to the system resolver.
	LookupIP func(ctx context.Context, host string) ([]net.IP, error)
	// ResolveTimeout bounds DNS lookups. Defaults to 5 seconds.
	ResolveTimeout time.Duration
}

// NewGuard builds a Guard.
func NewGuard(opts GuardOptions) *Guard {
	lookup := opts.LookupIP
	if lookup == nil {
		resolver := &net.Resolver{}
		lookup = func(ctx context.Context, host string) ([]net.IP, error) {
			addrs, err := resolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			ips := make([]net.IP, 0, len(addrs))
			for _, addr := range addrs {
				ips = append(ips, addr.IP)
			}
			return ips, nil
		}
	}

	timeout := opts.ResolveTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}

	return &Guard{
		allowPrivate: opts.AllowPrivate,
		lookupIP:     lookup,
		timeout:      timeout,
	}
}

// AllowPrivate reports whether the address classification is disabled.
func (g *Guard) AllowPrivate() bool { return g.allowPrivate }

// ValidateURL checks that a URL may be fetched and returns the parsed form.
//
// The checks run in the order that fails fastest: shape, then scheme, then the
// port, then the resolved addresses.
func (g *Guard) ValidateURL(ctx context.Context, raw string) (*url.URL, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return nil, fmt.Errorf("%w: no url given", ErrURLNotAllowed)
	}

	u, err := url.Parse(trimmed)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrURLNotAllowed, err)
	}

	if !u.IsAbs() {
		return nil, fmt.Errorf("%w: the url needs a scheme, such as https://", ErrURLNotAllowed)
	}
	if !allowedSchemes[strings.ToLower(u.Scheme)] {
		return nil, fmt.Errorf("%w: %q links are not supported", ErrURLNotAllowed, u.Scheme)
	}

	// Credentials in the URL are a way to smuggle authority past naive checks,
	// and this API has no reason to fetch anything that needs them.
	if u.User != nil {
		return nil, fmt.Errorf("%w: the url must not contain a username or password", ErrURLNotAllowed)
	}

	host := u.Hostname()
	if host == "" {
		return nil, fmt.Errorf("%w: the url has no host", ErrURLNotAllowed)
	}
	if !g.allowedPort(u) {
		return nil, fmt.Errorf("%w: only ports 80 and 443 are allowed", ErrURLNotAllowed)
	}

	// An address literal needs no resolution; anything else does.
	if ip := net.ParseIP(host); ip != nil {
		if err := g.checkIP(ip); err != nil {
			return nil, err
		}
		return u, nil
	}

	ctx, cancel := context.WithTimeout(ctx, g.timeout)
	defer cancel()

	ips, err := g.lookupIP(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("%w: %q: %v", ErrResolutionFailed, host, err)
	}
	if len(ips) == 0 {
		return nil, fmt.Errorf("%w: %q has no addresses", ErrResolutionFailed, host)
	}

	// Every address must be acceptable. A hostname that resolves to both a
	// public and a private address is a classic attempt to slip past a check
	// that only inspects the first result.
	for _, ip := range ips {
		if err := g.checkIP(ip); err != nil {
			return nil, err
		}
	}

	return u, nil
}

func (g *Guard) allowedPort(u *url.URL) bool {
	if g.allowPrivate {
		return true
	}
	return standardPorts[u.Port()]
}

// checkIP refuses addresses that are not globally routable.
func (g *Guard) checkIP(ip net.IP) error {
	if g.allowPrivate {
		return nil
	}
	if IsDisallowedIP(ip) {
		return fmt.Errorf("%w: %s is not a public address", ErrURLNotAllowed, ip)
	}
	return nil
}

// IsDisallowedIP reports whether an address must not be fetched.
//
// The list is everything that is not a globally routable unicast address: the
// loopback range, private networks, link-local (which includes the cloud
// metadata address 169.254.169.254), multicast, the unspecified address, and the
// shared address space carriers use for NAT.
func IsDisallowedIP(ip net.IP) bool {
	if ip == nil {
		return true
	}

	// Normalise IPv4-in-IPv6 so the predicates below see the right family.
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}

	switch {
	case ip.IsLoopback(),
		ip.IsPrivate(),
		ip.IsLinkLocalUnicast(),
		ip.IsLinkLocalMulticast(),
		ip.IsUnspecified(),
		ip.IsMulticast(),
		ip.IsInterfaceLocalMulticast():
		return true
	}

	// 100.64.0.0/10 is shared address space (carrier-grade NAT). It is not
	// routable on the public internet, and Go's IsPrivate does not cover it.
	return isSharedAddressSpace(ip)
}

// sharedAddressSpace is 100.64.0.0/10.
var sharedAddressSpace = &net.IPNet{
	IP:   net.IPv4(100, 64, 0, 0),
	Mask: net.CIDRMask(10, 32),
}

func isSharedAddressSpace(ip net.IP) bool {
	v4 := ip.To4()
	if v4 == nil {
		return false
	}
	return sharedAddressSpace.Contains(v4)
}

// Control returns a net.Dialer.Control hook that re-checks the address being
// connected to.
//
// This is the check that actually stops DNS rebinding: ValidateURL runs before
// the request, but the name can resolve differently a moment later. Control runs
// after resolution and immediately before connect, so it sees the real
// destination.
func (g *Guard) Control(network, address string, _ syscall.RawConn) error {
	if g.allowPrivate {
		return nil
	}
	if network != "tcp" && network != "tcp4" && network != "tcp6" {
		return fmt.Errorf("%w: refusing %s connections", ErrURLNotAllowed, network)
	}

	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return fmt.Errorf("%w: cannot read address %q", ErrURLNotAllowed, address)
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return fmt.Errorf("%w: %q is not an address", ErrURLNotAllowed, host)
	}
	if IsDisallowedIP(ip) {
		return fmt.Errorf("%w: refusing to connect to %s", ErrURLNotAllowed, ip)
	}
	return nil
}

// CheckRedirect validates each redirect hop.
//
// Following a redirect is a fresh request to a host the original page chose, so
// it needs the same scrutiny as the URL the user supplied. Without this, a public
// page could redirect the fetcher to an internal address.
func (g *Guard) CheckRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("%w: too many redirects (limit %d)", ErrURLNotAllowed, maxRedirects)
	}

	// A redirect that switches to another scheme must be refused too.
	if _, err := g.ValidateURL(req.Context(), req.URL.String()); err != nil {
		return fmt.Errorf("redirect refused: %w", err)
	}
	return nil
}
