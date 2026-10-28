package scrape

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/url"
	"regexp"
	"strings"

	"github.com/PuerkitoBio/goquery"
	"github.com/sj14/site2rss/internal/config"
)

// itemList is the part of a schema.org ItemList the feed needs. The field names
// are fixed by the standard, which is why json-ld needs no selectors.
type itemList struct {
	Type            string `json:"@type"`
	ItemListElement []struct {
		Item struct {
			Name        string `json:"name"`
			URL         string `json:"url"`
			Description string `json:"description"`
		} `json:"item"`
	} `json:"itemListElement"`
}

// appendJSONLDItems reads the entries of the schema.org ItemList a page embeds
// for search engines. Pages built by JavaScript often carry it in their initial
// html even though the visible list is missing there.
func appendJSONLDItems(items []Item, doc *goquery.Document, site config.Site, siteURL *url.URL) []Item {
	doc.Find(`script[type="application/ld+json"]`).Each(func(_ int, s *goquery.Selection) {
		// a page carries several blocks, an Organization or a BreadcrumbList among
		// them, and some of them do not fit the struct at all
		var list itemList
		if err := json.Unmarshal([]byte(s.Text()), &list); err != nil || list.Type != "ItemList" {
			return
		}

		for _, element := range list.ItemListElement {
			if element.Item.URL == "" {
				slog.Warn("item without a link", "site", site.Name, "title", element.Item.Name)
				continue
			}

			var (
				title       = normalizeSpace(element.Item.Name)
				description = normalizeSpace(element.Item.Description)
			)

			items = appendItem(items, site, siteURL, title, description, element.Item.URL)
		}
	})

	return items
}

// placeholder matches the {path} parts of a link template.
var placeholder = regexp.MustCompile(`\{([^}]+)\}`)

// ExtractJSON returns the entries of a JSON API response. Unlike a page that
// lacks matching elements, a response that does not parse or lacks the list is
// an error: the API answered with something else, an error page for instance,
// and the previous feed is better than an empty one.
func ExtractJSON(body []byte, site config.Site, siteURL *url.URL) ([]Item, error) {
	decoder := json.NewDecoder(bytes.NewReader(body))
	// keeps numbers as they were written, a year must not turn into 2.021e+03
	decoder.UseNumber()

	var data any
	if err := decoder.Decode(&data); err != nil {
		return nil, fmt.Errorf("parse JSON: %w", err)
	}

	entries, ok := valueAt(data, site.Selector.Item).([]any)
	if !ok {
		return nil, fmt.Errorf("%q is not an array", site.Selector.Item)
	}

	var items []Item

	for _, entry := range entries {
		var (
			title       = normalizeSpace(textAt(entry, site.Selector.Title))
			description = normalizeSpace(textAt(entry, site.Selector.Description))
		)

		link, ok := fillTemplate(site.Selector.Link, entry)
		if !ok {
			slog.Warn("item without a link", "site", site.Name, "title", title)
			continue
		}

		items = appendItem(items, site, siteURL, title, description, link)
	}

	return items, nil
}

// valueAt follows a dotted path such as "data.contents.data" into decoded JSON.
// It returns nil when a step of the path does not exist.
func valueAt(data any, path string) any {
	value := data

	for key := range strings.SplitSeq(path, ".") {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}

		value = object[key]
	}

	return value
}

// textAt returns the string or number at the path. An empty path, a missing
// value or anything else, an object for instance, yields an empty string.
func textAt(data any, path string) string {
	if path == "" {
		return ""
	}

	switch value := valueAt(data, path).(type) {
	case string:
		return value
	case json.Number:
		return value.String()
	default:
		return ""
	}
}

// fillTemplate replaces every {path} of the link template with the text at that
// path. It reports false when one of them is missing, since a link with a gap
// would point somewhere else entirely.
func fillTemplate(template string, entry any) (string, bool) {
	complete := true

	link := placeholder.ReplaceAllStringFunc(template, func(match string) string {
		path := placeholder.FindStringSubmatch(match)[1]

		text := textAt(entry, path)
		if text == "" {
			complete = false
		}

		return text
	})

	return link, complete
}
