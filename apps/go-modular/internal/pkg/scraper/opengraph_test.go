package scraper

import (
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func mustBase(t *testing.T, raw string) *url.URL {
	t.Helper()

	u, err := url.Parse(raw)
	require.NoError(t, err)
	return u
}

func parse(t *testing.T, page, base string) *OpenGraph {
	t.Helper()

	og, err := ParseOpenGraph(strings.NewReader(page), mustBase(t, base))
	require.NoError(t, err)
	return og
}

// --- a well-formed page -----------------------------------------------------

func TestParseOpenGraphReadsTheStandardTags(t *testing.T) {
	og := parse(t, `<!doctype html>
<html>
<head>
  <meta charset="utf-8">
  <meta property="og:title" content="One Piece">
  <meta property="og:image" content="https://cdn.example.com/cover.jpg">
  <meta property="og:site_name" content="Manga Reader">
  <meta property="og:description" content="Chapter 1050">
  <title>Chapter 1050 - Manga Reader</title>
</head>
<body><h1>Chapter 1050</h1></body>
</html>`, "https://reader.example.com/one-piece/1050")

	assert.Equal(t, "One Piece", og.Title)
	assert.Equal(t, "https://cdn.example.com/cover.jpg", og.Image)
	assert.Equal(t, "Manga Reader", og.SiteName)
	assert.Equal(t, "Chapter 1050", og.Description)
	assert.Empty(t, og.Warnings)
	assert.True(t, og.Complete())
}

// --- fallbacks --------------------------------------------------------------

func TestParseOpenGraphFallsBackToTheDocumentTitle(t *testing.T) {
	og := parse(t, `<html><head><title>Chapter 1050 - One Piece</title></head><body></body></html>`,
		"https://reader.example.com/one-piece/1050")

	assert.Equal(t, "Chapter 1050 - One Piece", og.Title)
	assert.Contains(t, og.Warnings, "no cover image found")
	assert.False(t, og.Complete())
}

func TestParseOpenGraphPrefersOpenGraphOverTwitterAndTitle(t *testing.T) {
	og := parse(t, `<html><head>
	  <title>Bare title</title>
	  <meta name="twitter:title" content="Twitter title">
	  <meta property="og:title" content="OpenGraph title">
	</head><body></body></html>`, "https://example.com/x")

	assert.Equal(t, "OpenGraph title", og.Title)
}

func TestParseOpenGraphFallsBackToTwitterTags(t *testing.T) {
	og := parse(t, `<html><head>
	  <title>Bare title</title>
	  <meta name="twitter:title" content="Twitter title">
	  <meta name="twitter:image" content="https://cdn.example.com/twitter.jpg">
	</head><body></body></html>`, "https://example.com/x")

	assert.Equal(t, "Twitter title", og.Title)
	assert.Equal(t, "https://cdn.example.com/twitter.jpg", og.Image)
}

func TestParseOpenGraphPrefersTheOgImageOverTwitter(t *testing.T) {
	og := parse(t, `<html><head>
	  <meta property="og:image" content="https://cdn.example.com/og.jpg">
	  <meta name="twitter:image" content="https://cdn.example.com/twitter.jpg">
	</head><body></body></html>`, "https://example.com/x")

	assert.Equal(t, "https://cdn.example.com/og.jpg", og.Image)
}

func TestParseOpenGraphAcceptsImageVariants(t *testing.T) {
	for name, tag := range map[string]string{
		"og:image":            `<meta property="og:image" content="https://cdn.example.com/a.jpg">`,
		"og:image:url":        `<meta property="og:image:url" content="https://cdn.example.com/a.jpg">`,
		"og:image:secure_url": `<meta property="og:image:secure_url" content="https://cdn.example.com/a.jpg">`,
		"twitter:image:src":   `<meta name="twitter:image:src" content="https://cdn.example.com/a.jpg">`,
	} {
		t.Run(name, func(t *testing.T) {
			og := parse(t, "<html><head>"+tag+"</head><body></body></html>", "https://example.com/x")
			assert.Equal(t, "https://cdn.example.com/a.jpg", og.Image)
		})
	}
}

// --- relative and unusable images -------------------------------------------

// Relative cover images are common, so resolving them is the difference between
// a preview with a picture and one without.
func TestParseOpenGraphResolvesRelativeImagesAgainstTheFetchedURL(t *testing.T) {
	cases := map[string]string{
		"/covers/one-piece.jpg":       "https://reader.example.com/covers/one-piece.jpg",
		"covers/one-piece.jpg":        "https://reader.example.com/manga/one-piece/covers/one-piece.jpg",
		"./covers/one-piece.jpg":      "https://reader.example.com/manga/one-piece/covers/one-piece.jpg",
		"../shared/cover.jpg":         "https://reader.example.com/manga/shared/cover.jpg",
		"//cdn.example.com/cover.jpg": "https://cdn.example.com/cover.jpg",
	}

	for raw, want := range cases {
		t.Run(raw, func(t *testing.T) {
			og := parse(t,
				`<html><head><meta property="og:image" content="`+raw+`"></head><body></body></html>`,
				"https://reader.example.com/manga/one-piece/chapter-1050")

			assert.Equal(t, want, og.Image)
		})
	}
}

func TestParseOpenGraphDropsUnusableImages(t *testing.T) {
	for name, raw := range map[string]string{
		"data uri":   "data:image/png;base64,iVBORw0KGgo=",
		"javascript": "javascript:alert(1)",
		"file path":  "file:///etc/passwd",
		"empty":      "",
	} {
		t.Run(name, func(t *testing.T) {
			og := parse(t,
				`<html><head><meta property="og:image" content="`+raw+`"></head><body></body></html>`,
				"https://example.com/x")

			assert.Empty(t, og.Image, "%q should not become a cover image", raw)
			assert.NotEmpty(t, og.Warnings)
		})
	}
}

// A bare filename is a valid relative reference, not a broken one, so it must be
// resolved against the page URL rather than discarded.
func TestParseOpenGraphResolvesABareFilename(t *testing.T) {
	og := parse(t,
		`<html><head><meta property="og:image" content="cover.jpg"></head><body></body></html>`,
		"https://example.com/manga/chapter-1")

	assert.Equal(t, "https://example.com/manga/cover.jpg", og.Image)
	assert.NotContains(t, og.Warnings, "cover image was not a usable http(s) address",
		"a relative reference is resolvable, not broken")
}

func TestParseOpenGraphWithoutABaseDropsRelativeImages(t *testing.T) {
	// Without a base there is nothing to resolve against, so a relative path
	// cannot be trusted to mean anything.
	og, err := ParseOpenGraph(strings.NewReader(
		`<html><head><meta property="og:image" content="/cover.jpg"></head><body></body></html>`), nil)
	require.NoError(t, err)

	assert.Empty(t, og.Image)
}

// --- robustness -------------------------------------------------------------

func TestParseOpenGraphToleratesAwkwardMarkup(t *testing.T) {
	for name, page := range map[string]string{
		"no head":         `<html><body><h1>Chapter 1</h1></body></html>`,
		"no title":        `<html><head><meta property="og:title" content="Still fine"></head></html>`,
		"unclosed meta":   `<html><head><meta property="og:title" content="Unclosed"><body>x</body>`,
		"empty document":  ``,
		"not html at all": `{"json": true}`,
		"head then junk":  `<html><head><title>T</title></head><body><p>text</p></body></html>`,
	} {
		t.Run(name, func(t *testing.T) {
			og, err := ParseOpenGraph(strings.NewReader(page), mustBase(t, "https://example.com/x"))
			require.NoError(t, err, "a malformed page is not an error, just thin metadata")
			require.NotNil(t, og)
		})
	}
}

// Metadata lives in the head, and stopping there avoids tokenising a whole
// chapter of images.
func TestParseOpenGraphStopsBeforeTheBody(t *testing.T) {
	og := parse(t, `<html><head>
	  <meta property="og:title" content="Head title">
	</head><body>
	  <meta property="og:title" content="Body title that must be ignored">
	</body></html>`, "https://example.com/x")

	assert.Equal(t, "Head title", og.Title)
}

// A site may repeat a tag; the first is the authoritative one.
func TestParseOpenGraphKeepsTheFirstOccurrence(t *testing.T) {
	og := parse(t, `<html><head>
	  <meta property="og:title" content="First">
	  <meta property="og:title" content="Second">
	</head><body></body></html>`, "https://example.com/x")

	assert.Equal(t, "First", og.Title)
}

func TestParseOpenGraphIgnoresBlankValues(t *testing.T) {
	og := parse(t, `<html><head>
	  <meta property="og:title" content="   ">
	  <meta property="og:image" content="">
	  <title>Real title</title>
	</head><body></body></html>`, "https://example.com/x")

	assert.Equal(t, "Real title", og.Title, "a blank tag must not shadow a usable one")
}

func TestParseOpenGraphHandlesSingleQuotedAndMixedCaseAttributes(t *testing.T) {
	og := parse(t, `<html><head>
	  <META PROPERTY="og:title" CONTENT="Shouty tags">
	</head><body></body></html>`, "https://example.com/x")

	assert.Equal(t, "Shouty tags", og.Title)
}

func TestParseOpenGraphExposesTheRawMetadata(t *testing.T) {
	og := parse(t, `<html><head>
	  <meta property="og:title" content="One Piece">
	  <meta property="comic:chapter" content="1050">
	  <meta name="author" content="Eiichiro Oda">
	</head><body></body></html>`, "https://example.com/x")

	// Chapter detection and future features read from this map.
	assert.Equal(t, "1050", og.Metadata["comic:chapter"])
	assert.Equal(t, "Eiichiro Oda", og.Metadata["author"])
	assert.Equal(t, "One Piece", og.Metadata["og:title"])
}

func TestParseOpenGraphWarnsAboutMissingFields(t *testing.T) {
	og := parse(t, `<html><head><meta property="og:title" content="Only a title"></head><body></body></html>`,
		"https://example.com/x")

	assert.Equal(t, "Only a title", og.Title)
	assert.Contains(t, og.Warnings, "no cover image found")
	assert.False(t, og.Complete(), "a preview without a cover is partial")
}

func TestParseOpenGraphReportsCompletelyEmptyPages(t *testing.T) {
	og := parse(t, `<html><head></head><body></body></html>`, "https://example.com/x")

	assert.Empty(t, og.Title)
	assert.False(t, og.Complete())
	assert.NotEmpty(t, og.Warnings)
}

func TestOpenGraphCompleteRequiresBothTitleAndImage(t *testing.T) {
	assert.False(t, (&OpenGraph{Title: "T"}).Complete())
	assert.False(t, (&OpenGraph{Image: "https://example.com/i.jpg"}).Complete())
	assert.True(t, (&OpenGraph{Title: "T", Image: "https://example.com/i.jpg"}).Complete())
}
