package useragent

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// Real User-Agent strings, so the assertions reflect what actually arrives.
const (
	uaChromeMac = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Safari/537.36"
	uaSafariiOS = "Mozilla/5.0 (iPhone; CPU iPhone OS 18_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.5 Mobile/15E148 Safari/604.1"
	uaChromeA   = "Mozilla/5.0 (Linux; Android 15; Pixel 9) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/155.0.0.0 Mobile Safari/537.36"
	uaFirefoxW  = "Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:148.0) Gecko/20100101 Firefox/148.0"
	uaDart      = "Dart/3.13.5 (dart:io)"
	uaDartA     = "Dart/3.13.5 (dart:io) Android"
	uaDartI     = "Dart/3.13.5 (dart:io) iOS"
	uaGooglebot = "Mozilla/5.0 (compatible; Googlebot/2.1; +http://www.google.com/bot.html)"
	uaCurl      = "curl/8.7.1"
)

func TestParseIdentifiesBrowsers(t *testing.T) {
	for name, tc := range map[string]struct {
		ua       string
		label    string
		platform string
	}{
		"chrome on macOS":    {uaChromeMac, "Chrome on macOS", PlatformWeb},
		"safari on iOS":      {uaSafariiOS, "Safari on iOS", PlatformWeb},
		"chrome on Android":  {uaChromeA, "Chrome on Android", PlatformWeb},
		"firefox on Windows": {uaFirefoxW, "Firefox on Windows", PlatformWeb},
	} {
		t.Run(name, func(t *testing.T) {
			d := Parse(tc.ua)
			assert.Equal(t, tc.label, d.Label)
			assert.Equal(t, tc.platform, d.Platform)
			assert.NotEmpty(t, d.Browser)
			assert.NotEmpty(t, d.OS)
		})
	}
}

func TestParseRecognisesDesktopBrowserDespiteMobileOS(t *testing.T) {
	// A mobile browser is still the web build, not a native app.
	d := Parse(uaChromeA)
	assert.Equal(t, PlatformWeb, d.Platform)
	assert.True(t, d.Mobile)
}

func TestParseNamesTheFlutterClient(t *testing.T) {
	t.Run("no OS detail", func(t *testing.T) {
		d := Parse(uaDart)
		assert.Equal(t, dartClientName, d.Browser)
		assert.Equal(t, PlatformUnknown, d.Platform)
		assert.Equal(t, dartClientName, d.Label)
	})

	t.Run("Android in the header", func(t *testing.T) {
		d := Parse(uaDartA)
		assert.Equal(t, PlatformAndroid, d.Platform)
		assert.Equal(t, "ComicMark app on Android", d.Label)
	})

	t.Run("iOS in the header", func(t *testing.T) {
		d := Parse(uaDartI)
		assert.Equal(t, PlatformIOS, d.Platform)
		assert.Equal(t, "ComicMark app on iOS", d.Label)
	})
}

func TestParseFallsBackGracefully(t *testing.T) {
	t.Run("empty", func(t *testing.T) {
		d := Parse("")
		assert.Equal(t, unknownLabel, d.Label)
		assert.Equal(t, PlatformUnknown, d.Platform)
	})

	t.Run("whitespace only", func(t *testing.T) {
		assert.Equal(t, unknownLabel, Parse("   ").Label)
	})

	t.Run("unrecognised client", func(t *testing.T) {
		d := Parse(uaCurl)
		assert.Equal(t, PlatformWeb, d.Platform)
		assert.NotEmpty(t, d.Label)
	})
}

func TestParseFlagsBots(t *testing.T) {
	d := Parse(uaGooglebot)
	assert.True(t, d.Bot)
	assert.NotEmpty(t, d.Label)
}

func TestDescribePrefersTheExplicitPlatformHeader(t *testing.T) {
	// A Dart client cannot say which OS it is; the header can.
	d := Describe(uaDart, PlatformIOS)
	assert.Equal(t, PlatformIOS, d.Platform)
	assert.Equal(t, "ComicMark app on iOS", d.Label)

	// The header also wins when it disagrees with the inferred value.
	d = Describe(uaChromeMac, PlatformAndroid)
	assert.Equal(t, PlatformAndroid, d.Platform)
}

func TestDescribeIgnoresAnUnknownPlatformHeader(t *testing.T) {
	d := Describe(uaChromeMac, "toaster")
	assert.Equal(t, PlatformWeb, d.Platform, "an unrecognised header must fall back to inference")

	d = Describe(uaChromeMac, "")
	assert.Equal(t, PlatformWeb, d.Platform)

	// Case and padding should not matter.
	d = Describe(uaChromeMac, "  iOS  ")
	assert.Equal(t, PlatformIOS, d.Platform)
}

func TestNormalizePlatform(t *testing.T) {
	for _, in := range []string{"web", "WEB", " android ", "iOS"} {
		_, ok := NormalizePlatform(in)
		assert.True(t, ok, "%q should be accepted", in)
	}
	for _, in := range []string{"", "ios-simulator", "windows", "webview"} {
		_, ok := NormalizePlatform(in)
		assert.False(t, ok, "%q should be rejected", in)
	}
}
