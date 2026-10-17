package scraper

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func number(t *testing.T, raw string) *float64 {
	t.Helper()

	n, ok := parseNumber(raw)
	require.True(t, ok, "%q should parse as a number", raw)
	return n
}

// --- extraction -------------------------------------------------------------

// A URL slug is the most reliable source, because sites keep slugs stable for
// links while titles are written for humans.
func TestExtractChapterFromASlug(t *testing.T) {
	for slug, want := range map[string]float64{
		"/one-piece-chapter-1050":            1050,
		"/one-piece-chapter-1050/":           1050,
		"/manga/one-piece/chapter/1050":      1050,
		"/manga/one-piece/chapter/1050/":     1050,
		"/read/one-piece-chapter-1050-2":     1050,
		"/one-piece/1050":                    1050,
		"/one-piece/1050/":                   1050,
		"/one-piece/1050.html":               1050,
		"/one-piece/ch-1050":                 1050,
		"/one-piece-chapter-12.5":            12.5,
		"/one-piece-volume-100-chapter-1050": 1050,
		"/one-piece-episode-7":               7,
		"/one-piece-chapter-1050?ref=next":   1050,
		"/one-piece-chapter-1050#page":       1050,
	} {
		t.Run(slug, func(t *testing.T) {
			chapter := ExtractChapter(slug)
			require.True(t, chapter.Found(), "expected a chapter in %q", slug)
			assert.Equal(t, want, *chapter.Number)
			assert.Equal(t, SourceSlug, chapter.Source)
		})
	}
}

func TestExtractChapterFromATitle(t *testing.T) {
	for title, want := range map[string]float64{
		"Chapter 1050":             1050,
		"Chapter 1050 | One Piece": 1050,
		"One Piece Chapter 1050":   1050,
		"Ch. 12.5":                 12.5,
		"Ch 7":                     7,
		"Ep 7":                     7,
		"Episode 7":                7,
		"Vol 3":                    3,
		"Volume 12":                12,
		"Chapter: 1050":            1050,
		"Chapter - 1050":           1050,
		"One Piece 1050":           -1, // no label, so not a title match
	} {
		t.Run(title, func(t *testing.T) {
			chapter := ExtractChapter("", title)
			if want < 0 {
				assert.False(t, chapter.Found(), "a bare number after words is not a labelled chapter")
				return
			}
			require.True(t, chapter.Found(), "expected a chapter in %q", title)
			assert.Equal(t, want, *chapter.Number)
			assert.Equal(t, title, chapter.Text, "the site's own wording should be preserved")
		})
	}
}

// A title that is nothing but a number is how some sites write it.
func TestExtractChapterFromABareNumericTitle(t *testing.T) {
	for title, want := range map[string]float64{
		"1050":   1050,
		" 1050 ": 1050,
		"12.5":   12.5,
	} {
		t.Run(title, func(t *testing.T) {
			chapter := ExtractChapter("", title)
			require.True(t, chapter.Found())
			assert.Equal(t, want, *chapter.Number)
		})
	}
}

func TestExtractChapterReportsWhenThereIsNoNumber(t *testing.T) {
	for name, tc := range map[string]struct {
		slug   string
		titles []string
	}{
		"prologue":           {"", []string{"Prologue"}},
		"no number anywhere": {"/one-piece/prologue", []string{"One Piece - Prologue"}},
		"empty":              {"", nil},
		"just a comic name":  {"/one-piece", []string{"One Piece"}},
		"roman numerals":     {"/one-piece/chapter-XX", []string{"Chapter XX"}},
	} {
		t.Run(name, func(t *testing.T) {
			chapter := ExtractChapter(tc.slug, tc.titles...)

			assert.False(t, chapter.Found(),
				"an unparseable chapter must leave the number nil so the user is asked")
			assert.Nil(t, chapter.Number)
			assert.Equal(t, SourceNone, chapter.Source)
		})
	}
}

// A URL carrying both a volume and a chapter must yield the chapter: that is what
// a reader tracks.
func TestExtractChapterPrefersAChapterOverAVolume(t *testing.T) {
	chapter := ExtractChapter("/one-piece/volume-100-chapter-1050")

	require.True(t, chapter.Found())
	assert.Equal(t, float64(1050), *chapter.Number)
}

// The slug wins over the title, because the title is often decorated or missing
// the number entirely.
func TestExtractChapterPrefersTheSlug(t *testing.T) {
	chapter := ExtractChapter("/one-piece-chapter-1050", "One Piece")

	require.True(t, chapter.Found())
	assert.Equal(t, float64(1050), *chapter.Number)
	assert.Equal(t, SourceSlug, chapter.Source)
}

func TestExtractChapterFallsBackThroughTitlesInOrder(t *testing.T) {
	// The first title has no number; the second does.
	chapter := ExtractChapter("", "One Piece", "One Piece - Chapter 1050")

	require.True(t, chapter.Found())
	assert.Equal(t, float64(1050), *chapter.Number)
	assert.Equal(t, "One Piece - Chapter 1050", chapter.Text)
}

func TestExtractChapterSkipsAWordThatMerelyStartsWithAChapterLabel(t *testing.T) {
	// "childhood" and "character" must not be read as chapter labels.
	chapter := ExtractChapter("", "A character study of childhood")

	assert.False(t, chapter.Found())
}

// --- increment --------------------------------------------------------------

func TestIncrementPreservesTheSitesWording(t *testing.T) {
	for name, tc := range map[string]struct {
		text   string
		number string
		want   string
	}{
		"chapter":          {"Chapter 1050", "1050", "Chapter 1051"},
		"chapter with sep": {"Chapter: 1050", "1050", "Chapter: 1051"},
		"abbreviated":      {"Ch. 12.5", "12.5", "Ch. 13.5"},
		"episode":          {"Ep 7", "7", "Ep 8"},
		"volume":           {"Vol 3", "3", "Vol 4"},
		"with a suffix":    {"One Piece Chapter 1050 - Read Online", "1050", "One Piece Chapter 1051 - Read Online"},
		"bare number":      {"1050", "1050", "1051"},
	} {
		t.Run(name, func(t *testing.T) {
			text, next, ok := Increment(tc.text, number(t, tc.number))

			require.True(t, ok)
			assert.Equal(t, tc.want, text)
			require.NotNil(t, next)
			assert.Equal(t, *number(t, tc.number)+1, *next)
		})
	}
}

// With no number there is nothing to add one to, so the caller must ask the user.
// A bookmark silently advanced to a wrong chapter is worse than one that asks.
func TestIncrementRefusesWhenThereIsNoNumber(t *testing.T) {
	for name, text := range map[string]string{
		"prologue": "Prologue",
		"empty":    "",
	} {
		t.Run(name, func(t *testing.T) {
			got, next, ok := Increment(text, nil)

			assert.False(t, ok)
			assert.Nil(t, next)
			assert.Equal(t, text, got, "the text must come back unchanged")
		})
	}
}

func TestIncrementKeepsDecimalPrecision(t *testing.T) {
	_, next, ok := Increment("Chapter 12.5", number(t, "12.5"))
	require.True(t, ok)
	assert.Equal(t, 13.5, *next)

	// A whole-number chapter stays whole.
	assert.Equal(t, "Chapter 1051", mustIncrement(t, "Chapter 1050", "1050"))
}

func TestIncrementWhenTheNumberCameFromTheSlug(t *testing.T) {
	// The text has no digits to advance, so the chapter alone is the honest
	// result; the comic's name lives in the bookmark's title.
	text, next, ok := Increment("One Piece", number(t, "1050"))

	require.True(t, ok)
	assert.Equal(t, "1051", text)
	assert.Equal(t, 1051.0, *next)
}

func TestIncrementAdvancesOncePerCall(t *testing.T) {
	text := "Chapter 1"
	current := number(t, "1")

	for expected := 2.0; expected <= 5; expected++ {
		var ok bool
		text, current, ok = Increment(text, current)
		require.True(t, ok)
		assert.Equal(t, expected, *current)
	}
	assert.Equal(t, "Chapter 5", text)
}

// --- number formatting ------------------------------------------------------

func TestFormatChapterNumber(t *testing.T) {
	for value, want := range map[float64]string{
		1050:  "1050",
		0:     "0",
		1:     "1",
		12.5:  "12.5",
		12.25: "12.25",
		1051:  "1051",
	} {
		assert.Equal(t, want, FormatChapterNumber(value), "for %v", value)
	}
}

func TestChapterHumanise(t *testing.T) {
	withText := Chapter{Text: "Chapter 1050", Number: number(t, "1050")}
	assert.Equal(t, "Chapter 1050 (1050)", withText.Humanise())

	numberOnly := Chapter{Number: number(t, "1050")}
	assert.Equal(t, "1050", numberOnly.Humanise())

	textOnly := Chapter{Text: "Prologue"}
	assert.Equal(t, "Prologue", textOnly.Humanise())

	assert.Equal(t, "unknown", Chapter{}.Humanise())
}

func mustIncrement(t *testing.T, text, current string) string {
	t.Helper()

	updated, _, ok := Increment(text, number(t, current))
	require.True(t, ok)
	return updated
}
