package scraper

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// Chapter sources, recorded so a surprising value can be traced back to where it
// came from.
const (
	// SourceSlug means the number came from the URL path.
	SourceSlug = "slug"
	// SourceTitle means it came from a page title or metadata value.
	SourceTitle = "title"
	// SourceNone means no number could be found.
	SourceNone = "none"
)

// labelledNumbers are tried in order, most specific first.
//
// The ordering matters: a URL like /one-piece-volume-100-chapter-1050/ contains
// two numbers, and a chapter tracker wants the chapter, not the volume. Volume
// is last for the same reason — it is the least likely to be what the reader is
// tracking.
var labelledNumbers = []*regexp.Regexp{
	regexp.MustCompile(`(?i)\b(?:chapter|chap)\s*[#:.\-]?\s*(\d+(?:\.\d+)?)\b`),
	regexp.MustCompile(`(?i)\b(?:episode|ep)\s*[#:.\-]?\s*(\d+(?:\.\d+)?)\b`),
	regexp.MustCompile(`(?i)\bch\s*[#:.\-]?\s*(\d+(?:\.\d+)?)\b`),
	regexp.MustCompile(`(?i)\b(?:volume|vol)\s*[#:.\-]?\s*(\d+(?:\.\d+)?)\b`),
}

// bareNumber matches a string that is nothing but a number, which is how many
// sites write the chapter in their <title> or their URL slug.
var bareNumber = regexp.MustCompile(`^\d+(?:\.\d+)?$`)

// Chapter is a chapter reference found on a page.
type Chapter struct {
	// Text is the chapter as the site wrote it, e.g. "Chapter 1050". Empty when
	// the site did not write one.
	Text string
	// Number is the parsed value, nil when the text carries no number. A nil
	// number is what makes the +1 action ask the user instead of guessing.
	Number *float64
	// Source records where Number came from: slug, title or none.
	Source string
}

// Found reports whether a number was extracted.
func (c Chapter) Found() bool { return c.Number != nil }

// ExtractChapter finds the chapter from a URL path and any available titles.
//
// The URL is consulted first because it is the most reliable: sites keep slugs
// stable for links, whereas titles are written for humans and are frequently
// decorated, translated, or missing the number entirely.
func ExtractChapter(slug string, titles ...string) Chapter {
	// 1. The URL slug.
	if number, ok := numberFromSlug(slug); ok {
		return Chapter{Number: number, Source: SourceSlug}
	}

	// 2. A title or metadata value.
	for _, title := range titles {
		if number, ok := numberFromLabel(title); ok {
			return Chapter{Text: strings.TrimSpace(title), Number: number, Source: SourceTitle}
		}
	}

	// 3. A title that is nothing but a number.
	for _, title := range titles {
		if trimmed := strings.TrimSpace(title); bareNumber.MatchString(trimmed) {
			if number, ok := parseNumber(trimmed); ok {
				return Chapter{Text: trimmed, Number: number, Source: SourceTitle}
			}
		}
	}

	return Chapter{Source: SourceNone}
}

// numberFromSlug reads a number out of a URL path.
func numberFromSlug(slug string) (*float64, bool) {
	path := slug
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		path = path[:i]
	}
	if path == "" {
		return nil, false
	}

	// A labelled number anywhere in the path.
	if number, ok := numberFromLabel(path); ok {
		return number, true
	}

	// Otherwise look at the last segment, which is how /one-piece/1050/ and
	// 1050.html are written.
	segments := strings.Split(strings.Trim(path, "/"), "/")
	for i := len(segments) - 1; i >= 0; i-- {
		segment := strings.TrimSuffix(segments[i], ".html")
		if bareNumber.MatchString(segment) {
			return parseNumber(segment)
		}
	}

	return nil, false
}

// numberFromLabel reads the number that follows a chapter-ish label.
func numberFromLabel(s string) (*float64, bool) {
	for _, pattern := range labelledNumbers {
		matches := pattern.FindAllStringSubmatch(s, -1)
		if len(matches) == 0 {
			continue
		}
		// The last match wins: in "vol 3 ch 1050" the chapter comes last, and
		// in a chapter-range title the later number is the current chapter.
		if number, ok := parseNumber(matches[len(matches)-1][1]); ok {
			return number, true
		}
	}
	return nil, false
}

func parseNumber(raw string) (*float64, bool) {
	value, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		return nil, false
	}
	return &value, true
}

// Increment advances a chapter by one.
//
// It returns the rewritten text, the new number, and whether it could be done at
// all. When the current number is nil there is nothing to add one to, so the
// caller must ask the user to type the chapter instead of guessing: a bookmark
// silently incremented to a wrong chapter is worse than one that asks.
//
// The rewrite replaces only the digits, so the site's own wording is preserved:
// "Chapter 1050" becomes "Chapter 1051", and "Ch. 12.5" becomes "Ch. 12.6".
func Increment(text string, number *float64) (string, *float64, bool) {
	if number == nil {
		return text, nil, false
	}

	next := *number + 1
	replacement := FormatChapterNumber(next)
	trimmed := strings.TrimSpace(text)

	// Replace the labelled number in place, keeping the surrounding wording.
	for _, pattern := range labelledNumbers {
		if loc := pattern.FindStringSubmatchIndex(trimmed); loc != nil && loc[2] >= 0 {
			updated := trimmed[:loc[2]] + replacement + trimmed[loc[3]:]
			return updated, &next, true
		}
	}

	// A title that is only a number is replaced wholesale.
	if bareNumber.MatchString(trimmed) {
		return replacement, &next, true
	}

	// The number came from the URL, so there is no number in the text to
	// advance. The chapter alone is the honest value; the comic's name lives in
	// the bookmark's title.
	return replacement, &next, true
}

// FormatChapterNumber renders a chapter number the way a site would.
//
// Whole numbers lose their decimal part, because "1051" is what a reader expects
// and "1051.0" looks like a mistake. Decimals are kept, since half-chapters are
// real.
func FormatChapterNumber(value float64) string {
	if math.Trunc(value) == value && math.Abs(value) < 1e15 {
		return strconv.FormatInt(int64(value), 10)
	}
	return strconv.FormatFloat(value, 'f', -1, 64)
}

// Humanise reports a chapter for a log line or an error message.
func (c Chapter) Humanise() string {
	switch {
	case c.Text != "" && c.Number != nil:
		return fmt.Sprintf("%s (%s)", c.Text, FormatChapterNumber(*c.Number))
	case c.Number != nil:
		return FormatChapterNumber(*c.Number)
	case c.Text != "":
		return c.Text
	default:
		return "unknown"
	}
}
