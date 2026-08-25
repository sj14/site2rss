package scrape

import (
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// Selectors are css plus the pseudo elements scrapy introduced for the same
// problem, which say what to read from a match:
//
//	h3::text            the text, and the default when nothing is said
//	a::attr(href)       an attribute
//	::attr(data-url)    an attribute of the matched item itself
//
// Neither "::attr(" nor "::text" can occur in real css, so telling the two parts
// apart needs no guessing.

// value returns what the first match of the selector yields. Taking a single
// element is what keeps a teaser that carries its title twice (arte does) from
// ending up as "TitleTitle".
func value(s *goquery.Selection, selector string) string {
	found := values(s, selector)
	if len(found) == 0 {
		return ""
	}

	return found[0]
}

// values returns what every match of the selector yields. Matches that do not
// carry the requested attribute are skipped.
func values(s *goquery.Selection, selector string) []string {
	if selector == "" {
		return nil
	}

	css, attr := splitSelector(selector)

	// Find only ever looks at descendants. A selector that is nothing but a pseudo
	// element therefore means the matched item itself, which is where a card tends
	// to carry its own data-url.
	matches := s
	if css != "" {
		matches = s.Find(css)
	}

	var found []string

	matches.Each(func(_ int, el *goquery.Selection) {
		if attr == "" {
			found = append(found, el.Text())
			return
		}

		if val, ok := el.Attr(attr); ok {
			found = append(found, val)
		}
	})

	return found
}

// splitSelector separates the css part from the pseudo element. An empty attr
// means the text, which is also what a selector without a pseudo element yields.
func splitSelector(selector string) (css, attr string) {
	if before, after, found := strings.Cut(selector, "::attr("); found {
		attr, _, _ = strings.Cut(after, ")")
		return before, attr
	}

	return strings.TrimSuffix(selector, "::text"), ""
}

// normalizeSpace trims the value and collapses runs of whitespace into a single
// space, since the markup indentation ends up inside the extracted text.
func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
