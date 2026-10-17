// Package useragent turns request metadata into a human-readable description of
// the device a session belongs to.
//
// The label appears in the app's "Devices & sessions" list, where it is the only
// thing letting a user recognise which entry is the phone they lost. A browser
// User-Agent is a good signal; the Dart HTTP client used by the Flutter app is
// not, so clients also send an explicit X-Client-Platform header, which takes
// precedence when present.
package useragent

import (
	"strings"

	"github.com/mileusna/useragent"
)

// Platform identifies which client build created a session.
const (
	PlatformWeb     = "web"
	PlatformAndroid = "android"
	PlatformIOS     = "ios"
	PlatformUnknown = "unknown"
)

// unknownLabel is used when nothing at all can be inferred.
const unknownLabel = "Unknown device"

// Device is a description of the client behind a request.
type Device struct {
	// Label is the human-readable description shown to the user,
	// e.g. "Chrome on macOS".
	Label string
	// Browser is the client application name, e.g. "Chrome".
	Browser string
	// OS is the operating system name, e.g. "macOS".
	OS string
	// Platform is web, android, ios or unknown.
	Platform string
	// Mobile reports whether the client is a mobile device.
	Mobile bool
	// Bot reports whether the client identified itself as a crawler.
	Bot bool
}

// Parse derives a Device from a User-Agent string alone.
func Parse(ua string) Device {
	trimmed := strings.TrimSpace(ua)
	if trimmed == "" {
		return Device{Label: unknownLabel, Platform: PlatformUnknown}
	}

	parsed := useragent.Parse(trimmed)

	d := Device{
		Browser: clean(parsed.Name),
		OS:      clean(parsed.OS),
		Mobile:  parsed.Mobile,
		Bot:     parsed.Bot,
	}

	// The Dart HTTP client (used by the Flutter app) reports itself as
	// "Dart/<version> (dart:io)" and carries no browser or OS detail. Fall back
	// to reading the platform out of the raw header.
	if d.Browser == "" || strings.EqualFold(d.Browser, "Dart") {
		d.Browser = dartClientName
		d.OS = dartOS(trimmed)
	}

	d.Platform = platformFrom(d)

	switch {
	case d.Bot:
		d.Label = label(d.Browser)
	case d.Browser != "" && d.OS != "":
		d.Label = d.Browser + " on " + d.OS
	default:
		d.Label = label(d.Browser)
	}
	return d
}

// dartClientName is what a Dart-based client is called in the UI. ComicMark's
// Flutter app is the only such client that talks to this API.
const dartClientName = "ComicMark app"

// dartOS sniffs the OS out of a Dart client's User-Agent. The Dart runtime does
// not disclose this, but the version string commonly includes it on mobile.
func dartOS(ua string) string {
	lower := strings.ToLower(ua)
	switch {
	case strings.Contains(lower, "android"):
		return "Android"
	case strings.Contains(lower, "iphone"), strings.Contains(lower, "ipad"), strings.Contains(lower, "ios"):
		return "iOS"
	case strings.Contains(lower, "macos"), strings.Contains(lower, "mac os"):
		return "macOS"
	case strings.Contains(lower, "windows"):
		return "Windows"
	case strings.Contains(lower, "linux"):
		return "Linux"
	default:
		return ""
	}
}

// platformFrom infers the client platform from a User-Agent-only description.
func platformFrom(d Device) string {
	switch {
	case d.Bot:
		return PlatformWeb
	case d.Browser == dartClientName:
		switch d.OS {
		case "Android":
			return PlatformAndroid
		case "iOS":
			return PlatformIOS
		default:
			return PlatformUnknown
		}
	default:
		// Everything else reaching the API with a browser-style User-Agent is
		// the web build.
		return PlatformWeb
	}
}

// Describe combines a User-Agent with the optional X-Client-Platform header.
// An explicit, recognised header wins over inference, because it is what the
// client itself knows to be true.
func Describe(ua, platformHeader string) Device {
	d := Parse(ua)

	if p, ok := NormalizePlatform(platformHeader); ok {
		d.Platform = p
		// A Dart client whose platform the header pins down can be named
		// properly instead of "ComicMark app" with no OS.
		if d.Browser == dartClientName {
			if name := platformName(p); name != "" {
				d.OS = name
				d.Label = d.Browser + " on " + d.OS
			}
		}
	}

	return d
}

// NormalizePlatform validates an X-Client-Platform value.
func NormalizePlatform(value string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case PlatformWeb:
		return PlatformWeb, true
	case PlatformAndroid:
		return PlatformAndroid, true
	case PlatformIOS:
		return PlatformIOS, true
	default:
		return "", false
	}
}

// platformName maps a platform to the OS name shown in a device label.
func platformName(platform string) string {
	switch platform {
	case PlatformAndroid:
		return "Android"
	case PlatformIOS:
		return "iOS"
	case PlatformWeb:
		return ""
	default:
		return ""
	}
}

// label builds a label from whatever is known, degrading gracefully.
func label(browser string) string {
	if browser == "" {
		return unknownLabel
	}
	return browser
}

// clean trims and drops placeholder values the parser uses for "unknown".
func clean(s string) string {
	s = strings.TrimSpace(s)
	if strings.EqualFold(s, "unknown") {
		return ""
	}
	return s
}
