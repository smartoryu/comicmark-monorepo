package scraper

// Fixture-driven tests.
//
// The pages in testdata/ are saved copies of the shapes real comic sites publish,
// so a parser change that breaks one of them fails here rather than in front of a
// user. They also document what "typical" means for this parser, which the
// synthetic strings in the other tests cannot.

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fixtureCase describes one saved page and what should be extracted from it.
type fixtureCase struct {
	file string
	// pageURL is where the page was served from, which relative covers resolve
	// against and which the chapter slug is read from.
	pageURL string

	wantTitle  string
	wantCover  string
	wantSite   string
	wantNumber *float64
	wantSource string
}

func fixtures() []fixtureCase {
	return []fixtureCase{
		{
			file:       "onepiece_og.html",
			pageURL:    "https://mangareader.example/one-piece-chapter-1050",
			wantTitle:  "One Piece",
			wantCover:  "https://cdn.mangareader.example/one-piece/1050/cover.jpg",
			wantSite:   "Manga Reader",
			wantNumber: floatPointer(1050),
			wantSource: SourceSlug,
		},
		{
			// No OpenGraph tags, a Twitter card, and a relative cover: the shape
			// that catches a parser which only looks for og:*.
			file:       "twitter_card_only.html",
			pageURL:    "https://somecomic.example/read/some-comic-chapter-12.5",
			wantTitle:  "Some Comic",
			wantCover:  "https://somecomic.example/assets/covers/some-comic.jpg",
			wantNumber: floatPointer(12.5),
			wantSource: SourceSlug,
		},
		{
			// Nothing numeric anywhere, so the parser must say so rather than
			// guess.
			file:       "no_metadata.html",
			pageURL:    "https://somecomic.example/read/some-comic/prologue",
			wantTitle:  "Prologue - Some Comic",
			wantNumber: nil,
			wantSource: SourceNone,
		},
		{
			// Shouty attributes, a duplicate tag, a blank value, and a cover that
			// needs resolving one level up.
			file:       "awkward_markup.html",
			pageURL:    "https://komik.example/baca/komik-contoh-chapter-7",
			wantTitle:  "Komik Contoh",
			wantCover:  "https://komik.example/covers/komik-contoh.jpg",
			wantNumber: floatPointer(7),
			wantSource: SourceSlug,
		},
	}
}

func TestParseOpenGraphAgainstSavedFixtures(t *testing.T) {
	for _, tc := range fixtures() {
		t.Run(tc.file, func(t *testing.T) {
			page := readFixture(t, tc.file)

			og, err := ParseOpenGraph(strings.NewReader(page), mustBase(t, tc.pageURL))
			require.NoError(t, err)

			assert.Equal(t, tc.wantTitle, og.Title)
			assert.Equal(t, tc.wantCover, og.Image)
			if tc.wantSite != "" {
				assert.Equal(t, tc.wantSite, og.SiteName)
			}

			// The chapter is read from the page URL, which is what a saved link
			// would look like.
			chapter := ExtractChapter(mustBase(t, tc.pageURL).Path, og.Title)
			assert.Equal(t, tc.wantSource, chapter.Source)

			if tc.wantNumber == nil {
				assert.False(t, chapter.Found(),
					"%s has no chapter number, so none may be invented", tc.file)
			} else {
				require.True(t, chapter.Found(), "%s should yield a chapter", tc.file)
				assert.Equal(t, *tc.wantNumber, *chapter.Number)
			}
		})
	}
}

// Advancing a fixture-derived chapter must preserve the site's own wording.
func TestIncrementOnFixtureValues(t *testing.T) {
	for _, tc := range fixtures() {
		if tc.wantNumber == nil {
			continue
		}

		t.Run(tc.file, func(t *testing.T) {
			page := readFixture(t, tc.file)
			og, err := ParseOpenGraph(strings.NewReader(page), mustBase(t, tc.pageURL))
			require.NoError(t, err)

			chapter := ExtractChapter(mustBase(t, tc.pageURL).Path, og.Title)

			text, next, ok := Increment(chapter.Text, chapter.Number)
			require.True(t, ok)
			require.NotNil(t, next)

			assert.Equal(t, *tc.wantNumber+1, *next)
			// When the site wrote a chapter, its wording survives the edit.
			if chapter.Text != "" {
				assert.Contains(t, text, FormatChapterNumber(*next))
			}
		})
	}
}

// --- helpers ----------------------------------------------------------------

func readFixture(t *testing.T, name string) string {
	t.Helper()

	path := filepath.Join("testdata", name)
	data, err := os.ReadFile(path)
	require.NoError(t, err, "fixture %s should exist; is testdata/ ignored by git?", path)

	return string(data)
}

func floatPointer(value float64) *float64 { return &value }
