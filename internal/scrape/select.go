package scrape

import (
	"strings"

	"github.com/PuerkitoBio/goquery"
)

// getFields returns one value per element the selector matches. A selector of the
// form "attr@css" reads that attribute, otherwise the element text is used.
// Elements without the attribute are skipped.
func getFields(s *goquery.Selection, selector string) []string {
	attr, css, isAttr := strings.Cut(selector, "@")
	if !isAttr {
		css = selector
	}

	var values []string

	s.Find(css).Each(func(i int, el *goquery.Selection) {
		if !isAttr {
			values = append(values, el.Text())
			return
		}

		if val, ok := el.Attr(attr); ok {
			values = append(values, val)
		}
	})

	return values
}

// getField returns the first value the selector matches, or an empty string.
// Taking a single element is what keeps a teaser that carries its title twice
// (arte does) from ending up as "TitleTitle".
func getField(s *goquery.Selection, selector string) string {
	values := getFields(s, selector)
	if len(values) == 0 {
		return ""
	}

	return values[0]
}

// normalizeSpace trims the value and collapses runs of whitespace into a single
// space, since the markup indentation ends up inside the extracted text.
func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}
