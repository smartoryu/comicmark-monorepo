package scraper

import (
	"fmt"
	"io"
	"net/url"
	"strings"

	"golang.org/x/net/html"
)

// Metadata keys, in the order each is consulted. A page may publish any
// combination, so every consumer walks the list rather than trusting one name.
var (
	titleKeys = []string{"og:title", "twitter:title"}
	imageKeys = []string{"og:image", "og:image:url", "og:image:secure_url", "twitter:image", "twitter:image:src"}
	siteKeys  = []string{"og:site_name", "application-name"}
	descKeys  = []string{"og:description", "twitter:description", "description"}
)

// OpenGraph is the metadata read from a page.
type OpenGraph struct {
	// Title is the page title, preferring OpenGraph over a bare <title>.
	Title string
	// Image is the cover image, resolved to an absolute http(s) URL.
	Image string
	// SiteName is the publisher, when the page declares one.
	SiteName string
	// Description is the summary, when the page declares one.
	Description string

	// Metadata is every meta tag found, keyed by its property or name. Chapter
	// detection reads from it, and it makes an unexpected page shape debuggable.
	Metadata map[string]string

	// Warnings describes what was missing or unusable, so the client can tell a
	// complete preview from a partial one.
	Warnings []string
}

// ParseOpenGraph reads metadata from an HTML document.
//
// base is the address the document was fetched from, which relative image URLs
// are resolved against. It must be the URL after redirects, not the one the user
// supplied: a site that redirects from a vanity path to a canonical chapter URL
// usually serves its relative assets against the latter.
func ParseOpenGraph(r io.Reader, base *url.URL) (*OpenGraph, error) {
	meta, title, err := scanHead(r)
	if err != nil {
		return nil, err
	}

	og := &OpenGraph{Metadata: meta}

	og.Title = firstNonEmpty(meta, titleKeys...)
	if og.Title == "" {
		// A bare <title> is the most common fallback, and it is usually the only
		// thing a site provides when it has no OpenGraph tags at all.
		og.Title = strings.TrimSpace(title)
		if og.Title == "" {
			og.addWarning("no title found")
		}
	}

	og.SiteName = firstNonEmpty(meta, siteKeys...)
	og.Description = firstNonEmpty(meta, descKeys...)
	og.Image = resolveImage(firstNonEmpty(meta, imageKeys...), base)

	if og.Image == "" {
		if raw := firstNonEmpty(meta, imageKeys...); raw != "" {
			og.addWarning("cover image was not a usable http(s) address")
		} else {
			og.addWarning("no cover image found")
		}
	}

	return og, nil
}

// addWarning records a missing or unusable field.
func (og *OpenGraph) addWarning(message string) {
	og.Warnings = append(og.Warnings, message)
}

// Complete reports whether enough metadata was found for a preview the user can
// accept without editing anything.
func (og *OpenGraph) Complete() bool { return og.Title != "" && og.Image != "" }

// resolveImage turns a possibly relative image reference into an absolute URL.
//
// Relative cover images are common, so resolving them is the difference between
// a usable preview and a broken one. Anything that is not http(s) is dropped:
// a data: URI cannot be cached by an image client, and javascript: is never an
// image.
func resolveImage(raw string, base *url.URL) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}

	ref, err := url.Parse(raw)
	if err != nil {
		return ""
	}

	// Protocol-relative references ("//cdn.example/x.jpg") inherit the base
	// scheme, which ResolveReference handles once there is a base to resolve
	// against.
	resolved := ref
	if base != nil {
		resolved = base.ResolveReference(ref)
	}

	switch strings.ToLower(resolved.Scheme) {
	case "http", "https":
	default:
		return ""
	}
	if resolved.Host == "" {
		return ""
	}

	return resolved.String()
}

// firstNonEmpty returns the first key present in the metadata map with a
// non-blank value.
func firstNonEmpty(meta map[string]string, keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(meta[key]); value != "" {
			return value
		}
	}
	return ""
}

// scanHead walks a document and returns its meta tags and its <title> text.
//
// It stops at </head>, because OpenGraph metadata is a head-level concern and
// stopping early avoids tokenising a whole chapter of images and adverts.
func scanHead(r io.Reader) (map[string]string, string, error) {
	z := html.NewTokenizer(r)

	meta := make(map[string]string)
	var (
		title     string
		inTitle   bool
		titleDone bool
	)

	for {
		switch z.Next() {
		case html.ErrorToken:
			err := z.Err()
			if err == io.EOF {
				// Many pages omit </head>; reaching the end is not a failure.
				return meta, title, nil
			}
			return nil, "", fmt.Errorf("scraper: read html: %w", err)

		case html.StartTagToken, html.SelfClosingTagToken:
			name, hasAttr := z.TagName()
			tag := string(name)

			switch tag {
			case "head":
				// keep walking; this is just the entry point

			case "body":
				// No </head> was present. Metadata is behind us.
				return meta, title, nil

			case "title":
				if !titleDone {
					inTitle = true
				}

			case "meta":
				key, content := readMeta(z, hasAttr)
				if key == "" || content == "" {
					continue
				}
				// The first occurrence wins: sites sometimes repeat a tag, and
				// the first is the authoritative one.
				if _, seen := meta[key]; !seen {
					meta[key] = content
				}
			}

		case html.TextToken:
			if inTitle && !titleDone {
				if text := strings.TrimSpace(string(z.Text())); text != "" {
					title = text
					titleDone = true
				}
			}

		case html.EndTagToken:
			name, _ := z.TagName()
			switch string(name) {
			case "title":
				inTitle = false
				titleDone = true
			case "head":
				return meta, title, nil
			}
		}
	}
}

// readMeta extracts the property/name and content attributes of a meta tag.
func readMeta(z *html.Tokenizer, hasAttr bool) (key, content string) {
	if !hasAttr {
		return "", ""
	}

	for {
		attrKey, attrValue, more := z.TagAttr()
		switch strings.ToLower(string(attrKey)) {
		case "property", "name":
			// `name` covers twitter: and the plain description tag.
			if key == "" {
				key = strings.ToLower(strings.TrimSpace(string(attrValue)))
			}
		case "content":
			content = strings.TrimSpace(string(attrValue))
		}
		if !more {
			break
		}
	}

	return key, content
}
