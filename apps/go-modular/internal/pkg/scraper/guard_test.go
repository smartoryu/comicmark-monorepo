package scraper

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// staticResolver returns fixed addresses, so these tests do not depend on DNS.
func staticResolver(ips ...string) func(context.Context, string) ([]net.IP, error) {
	return func(context.Context, string) ([]net.IP, error) {
		out := make([]net.IP, 0, len(ips))
		for _, s := range ips {
			ip := net.ParseIP(s)
			if ip == nil {
				return nil, fmt.Errorf("bad test address %q", s)
			}
			out = append(out, ip)
		}
		return out, nil
	}
}

func denyingResolver(err error) func(context.Context, string) ([]net.IP, error) {
	return func(context.Context, string) ([]net.IP, error) { return nil, err }
}

// --- address classification -------------------------------------------------

// The ranges that matter: anything not globally routable must be refused, and a
// genuinely public address must not be.
func TestIsDisallowedIP(t *testing.T) {
	disallowed := map[string]string{
		"127.0.0.1":        "loopback",
		"127.255.255.254":  "loopback range",
		"::1":              "IPv6 loopback",
		"10.0.0.1":         "private class A",
		"10.255.255.255":   "private class A upper bound",
		"172.16.0.1":       "private class B lower bound",
		"172.31.255.254":   "private class B upper bound",
		"192.168.0.1":      "private class C",
		"192.168.255.254":  "private class C upper bound",
		"169.254.169.254":  "cloud metadata",
		"169.254.0.1":      "IPv4 link-local",
		"fe80::1":          "IPv6 link-local",
		"fc00::1":          "IPv6 unique local (fc00::/7)",
		"fd00::1":          "IPv6 unique local (fd00::/8)",
		"0.0.0.0":          "unspecified IPv4",
		"::":               "unspecified IPv6",
		"224.0.0.1":        "IPv4 multicast",
		"ff02::1":          "IPv6 multicast",
		"100.64.0.1":       "carrier-grade NAT",
		"100.127.255.254":  "carrier-grade NAT upper bound",
		"::ffff:127.0.0.1": "IPv4-mapped loopback",
		"::ffff:10.0.0.1":  "IPv4-mapped private",
	}

	for address, why := range disallowed {
		t.Run(address+" ("+why+")", func(t *testing.T) {
			ip := net.ParseIP(address)
			require.NotNil(t, ip, "test address must parse")
			assert.True(t, IsDisallowedIP(ip), "%s should be refused: %s", address, why)
		})
	}

	allowed := map[string]string{
		"93.184.216.34":   "example.com",
		"8.8.8.8":         "public resolver",
		"1.1.1.1":         "public resolver",
		"172.15.255.255":  "just below the private class B range",
		"172.32.0.1":      "just above the private class B range",
		"100.63.255.255":  "just below the shared range",
		"100.128.0.1":     "just above the shared range",
		"2606:4700::1111": "public IPv6",
	}

	for address, why := range allowed {
		t.Run(address+" ("+why+")", func(t *testing.T) {
			ip := net.ParseIP(address)
			require.NotNil(t, ip)
			assert.False(t, IsDisallowedIP(ip), "%s should be allowed: %s", address, why)
		})
	}

	assert.True(t, IsDisallowedIP(nil), "a nil address is not fetchable")
}

// --- URL validation ---------------------------------------------------------

func TestValidateURLRefusesUnsupportedShapes(t *testing.T) {
	g := NewGuard(GuardOptions{LookupIP: staticResolver("93.184.216.34")})

	for name, raw := range map[string]string{
		"empty":             "",
		"whitespace":        "   ",
		"no scheme":         "example.com/chapter/1",
		"file scheme":       "file:///etc/passwd",
		"ftp scheme":        "ftp://example.com/x",
		"gopher scheme":     "gopher://example.com/x",
		"data uri":          "data:text/html,<h1>hi</h1>",
		"javascript":        "javascript:alert(1)",
		"userinfo":          "http://user:pass@example.com/x",
		"username only":     "http://user@example.com/x",
		"no host":           "http:///path",
		"non standard port": "http://example.com:8080/x",
		"metadata by port":  "http://169.254.169.254:80/latest/meta-data/",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := g.ValidateURL(context.Background(), raw)
			require.Error(t, err)
			assert.True(t,
				errors.Is(err, ErrURLNotAllowed) || errors.Is(err, ErrResolutionFailed),
				"expected a refusal, got %v", err)
		})
	}
}

func TestValidateURLAcceptsOrdinaryComicLinks(t *testing.T) {
	g := NewGuard(GuardOptions{LookupIP: staticResolver("93.184.216.34")})

	for _, raw := range []string{
		"https://example.com/one-piece-chapter-1050",
		"http://example.com/manga/one-piece/1050",
		"https://example.com:443/chapter/1050",
		"http://example.com:80/chapter/1050",
		"https://sub.example.com/chapter/1050?a=1&b=2#page",
		"https://93.184.216.34/chapter/1050",
		"https://[2606:4700::1111]/chapter/1050",
	} {
		t.Run(raw, func(t *testing.T) {
			u, err := g.ValidateURL(context.Background(), raw)
			require.NoError(t, err)
			assert.NotNil(t, u)
		})
	}
}

func TestValidateURLRefusesPrivateDestinations(t *testing.T) {
	g := NewGuard(GuardOptions{LookupIP: staticResolver("93.184.216.34")})

	for name, raw := range map[string]string{
		"loopback literal":               "http://127.0.0.1:80/",
		"private literal":                "http://10.0.0.5/",
		"metadata literal":               "http://169.254.169.254/latest/meta-data/",
		"IPv6 loopback":                  "http://[::1]/",
		"IPv6 unique local":              "http://[fd00::1]/",
		"hostname resolving to loopback": "http://internal.example.com/",
	} {
		t.Run(name, func(t *testing.T) {
			guard := g
			if strings.Contains(raw, "internal.example.com") {
				guard = NewGuard(GuardOptions{LookupIP: staticResolver("127.0.0.1")})
			}

			_, err := guard.ValidateURL(context.Background(), raw)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrURLNotAllowed)
		})
	}
}

// A hostname resolving to a public *and* a private address is a standard way to
// slip past a check that only looks at the first result.
func TestValidateURLRefusesAHostThatMixesPublicAndPrivateAddresses(t *testing.T) {
	g := NewGuard(GuardOptions{LookupIP: staticResolver("93.184.216.34", "127.0.0.1")})

	_, err := g.ValidateURL(context.Background(), "https://mixed.example.com/x")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrURLNotAllowed)
}

func TestValidateURLDistinguishesResolutionFailure(t *testing.T) {
	g := NewGuard(GuardOptions{
		LookupIP: denyingResolver(errors.New("no such host")),
	})

	_, err := g.ValidateURL(context.Background(), "https://typo.example.invalid/x")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrResolutionFailed)
	assert.NotErrorIs(t, err, ErrURLNotAllowed,
		"a DNS failure is not the user's mistake and should read differently")
}

func TestValidateURLRefusesAHostWithNoAddresses(t *testing.T) {
	g := NewGuard(GuardOptions{LookupIP: staticResolver()})

	_, err := g.ValidateURL(context.Background(), "https://empty.example.com/x")

	require.Error(t, err)
	assert.ErrorIs(t, err, ErrResolutionFailed)
}

func TestValidateURLTrimsSurroundingSpace(t *testing.T) {
	g := NewGuard(GuardOptions{LookupIP: staticResolver("93.184.216.34")})

	// Pasting from a clipboard routinely brings a leading newline.
	u, err := g.ValidateURL(context.Background(), "  https://example.com/chapter/1050\n")
	require.NoError(t, err)
	assert.Equal(t, "example.com", u.Hostname())
}

// --- the development escape hatch -------------------------------------------

func TestAllowPrivateDisablesClassification(t *testing.T) {
	g := NewGuard(GuardOptions{AllowPrivate: true})

	for _, raw := range []string{
		"http://127.0.0.1:9099/fixture.html",
		"http://localhost:9099/fixture.html",
		"http://10.0.0.5:8080/x",
	} {
		t.Run(raw, func(t *testing.T) {
			_, err := g.ValidateURL(context.Background(), raw)
			require.NoError(t, err, "the development fixture runs on a private address")
		})
	}

	assert.True(t, g.AllowPrivate())
}

// --- dial-time re-check -----------------------------------------------------

// Control runs after resolution and immediately before connect, so it is what
// actually catches a name that resolved publicly a moment ago.
func TestControlRefusesPrivateDestinations(t *testing.T) {
	g := NewGuard(GuardOptions{LookupIP: staticResolver("93.184.216.34")})

	for _, address := range []string{
		"127.0.0.1:80",
		"10.0.0.5:443",
		"[::1]:80",
		"169.254.169.254:80",
		"[fd00::1]:8080",
	} {
		t.Run(address, func(t *testing.T) {
			err := g.Control("tcp", address, nil)
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrURLNotAllowed)
		})
	}

	assert.NoError(t, g.Control("tcp", "93.184.216.34:443", nil))
}

func TestControlAllowsEverythingWhenPrivateIsAllowed(t *testing.T) {
	g := NewGuard(GuardOptions{AllowPrivate: true})

	assert.NoError(t, g.Control("tcp", "127.0.0.1:9099", nil))
}

func TestControlRefusesNonTCPNetworks(t *testing.T) {
	g := NewGuard(GuardOptions{})

	assert.Error(t, g.Control("udp", "93.184.216.34:53", nil))
	assert.Error(t, g.Control("unix", "/var/run/socket", nil))
}

func TestControlRejectsAnAddressItCannotRead(t *testing.T) {
	g := NewGuard(GuardOptions{})

	require.Error(t, g.Control("tcp", "no-port", nil))
	require.Error(t, g.Control("tcp", "hostname:80", nil))
}

// --- redirects --------------------------------------------------------------

func TestCheckRedirectRevalidatesEveryHop(t *testing.T) {
	g := NewGuard(GuardOptions{LookupIP: staticResolver("93.184.216.34")})
	ctx := context.Background()

	t.Run("allows a public destination", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com/next", nil)
		require.NoError(t, err)

		assert.NoError(t, g.CheckRedirect(req, []*http.Request{{}}))
	})

	t.Run("refuses a redirect to a private address", func(t *testing.T) {
		// A permitted page redirecting the fetcher at an internal service is
		// exactly the attack the guard exists for.
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://127.0.0.1:80/admin", nil)
		require.NoError(t, err)

		err = g.CheckRedirect(req, []*http.Request{{}})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrURLNotAllowed)
	})

	t.Run("refuses a redirect to the metadata address", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet,
			"http://169.254.169.254/latest/meta-data/", nil)
		require.NoError(t, err)

		err = g.CheckRedirect(req, []*http.Request{{}})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrURLNotAllowed)
	})

	t.Run("refuses a redirect that changes scheme", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "file:///etc/passwd", nil)
		require.NoError(t, err)

		err = g.CheckRedirect(req, []*http.Request{{}})
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrURLNotAllowed)
	})

	t.Run("refuses an endless chain", func(t *testing.T) {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://example.com/next", nil)
		require.NoError(t, err)

		via := make([]*http.Request, maxRedirects)
		err = g.CheckRedirect(req, via)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "too many redirects")
	})
}

// --- integration through a real client --------------------------------------

// The three checks have to work together, not just individually, so this drives
// a real http.Client configured the way the scraper configures one.
func TestGuardProtectsARealRequest(t *testing.T) {
	// A server on loopback stands in for an internal service.
	internal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		_, _ = w.Write([]byte(`<html><head><meta property="og:title" content="Internal Secrets"></head></html>`))
	}))
	defer internal.Close()

	guard := NewGuard(GuardOptions{})
	dialer := &net.Dialer{Control: guard.Control}
	client := &http.Client{
		Transport:     &http.Transport{DialContext: dialer.DialContext},
		CheckRedirect: guard.CheckRedirect,
	}

	t.Run("refused before the request", func(t *testing.T) {
		_, err := guard.ValidateURL(context.Background(), internal.URL)
		require.Error(t, err)
		assert.ErrorIs(t, err, ErrURLNotAllowed)
	})

	t.Run("refused at dial time even if validation is skipped", func(t *testing.T) {
		// This is the DNS-rebinding case: validation passed on a public address
		// that now resolves to loopback.
		resp, err := client.Get(internal.URL)

		require.Error(t, err, "the dialer must refuse the connection")
		if resp != nil {
			_ = resp.Body.Close()
		}
		assert.ErrorIs(t, err, ErrURLNotAllowed)
	})

	t.Run("a public destination is fetched", func(t *testing.T) {
		// The allowance path is proven with private access enabled, since the
		// only server available in a unit test is on loopback.
		permissive := NewGuard(GuardOptions{AllowPrivate: true})
		permissiveDialer := &net.Dialer{Control: permissive.Control}
		permissiveClient := &http.Client{
			Transport:     &http.Transport{DialContext: permissiveDialer.DialContext},
			CheckRedirect: permissive.CheckRedirect,
		}

		_, err := permissive.ValidateURL(context.Background(), internal.URL)
		require.NoError(t, err)

		resp, err := permissiveClient.Get(internal.URL)
		require.NoError(t, err)
		defer func() { _ = resp.Body.Close() }()
		assert.Equal(t, http.StatusOK, resp.StatusCode)
	})
}
