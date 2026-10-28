package scrape

import (
	"net/url"
	"strings"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/sj14/site2rss/internal/config"
)

func TestExtractJSON(t *testing.T) {
	siteURL, err := url.Parse("https://api.example.com/lists/free")
	if err != nil {
		t.Fatal(err)
	}

	site := config.Site{
		Name:   "Test",
		Format: config.FormatJSON,
		Selector: config.Selector{
			Item:        "data.entries",
			Link:        "/movies/{id}",
			Title:       "title",
			Description: "year",
		},
	}

	body := `{"data": {"entries": [
		{"id": "a-film", "title": "  A  film ", "year": 2021},
		{"id": "a-film", "title": "A film again"},
		{"title": "No id"},
		{"id": "another-film", "title": "Another film", "year": {"from": 1999}}
	]}}`

	items, err := ExtractJSON([]byte(body), site, siteURL)
	if err != nil {
		t.Fatal(err)
	}

	want := []Item{
		{Title: "A film", Link: "https://api.example.com/movies/a-film", Description: "2021"},
		{Title: "Another film", Link: "https://api.example.com/movies/another-film", Description: ""},
	}

	if len(items) != len(want) {
		t.Fatalf("got %d items, want %d: %+v", len(items), len(want), items)
	}

	for i := range want {
		got := items[i]
		if got.Title != want[i].Title || got.Link != want[i].Link || got.Description != want[i].Description {
			t.Errorf("item %d = %+v, want %+v", i, got, want[i])
		}
	}
}

// TestExtractJSONFails covers responses the API sends instead of the list. They
// must fail the update, so the previous feed stays.
func TestExtractJSONFails(t *testing.T) {
	siteURL, err := url.Parse("https://api.example.com/lists/free")
	if err != nil {
		t.Fatal(err)
	}

	site := config.Site{
		Name:     "Test",
		Format:   config.FormatJSON,
		Selector: config.Selector{Item: "data.entries", Link: "/movies/{id}", Title: "title"},
	}

	tests := []string{
		"<!DOCTYPE html><html></html>",
		`{"data": {"entries": "<!DOCTYPE html>"}}`,
		`{"data": {}}`,
	}

	for _, body := range tests {
		if _, err := ExtractJSON([]byte(body), site, siteURL); err == nil {
			t.Errorf("ExtractJSON(%q): expected an error", body)
		}
	}
}

func TestAppendJSONLDItems(t *testing.T) {
	siteURL, err := url.Parse("https://tele5.de/filme")
	if err != nil {
		t.Fatal(err)
	}

	site := config.Site{Name: "Test", Format: config.FormatJSONLD}

	// the breadcrumbs are ListItems as well, but must not end up in the feed
	page := `<div id="root"></div>
		<script type="application/ld+json">{"@type": "BreadcrumbList", "itemListElement": [
			{"@type": "ListItem", "name": "Filme", "item": "https://tele5.de/filme"}
		]}</script>
		<script type="application/ld+json">not json</script>
		<script type="application/ld+json">{"@type": "ItemList", "itemListElement": [
			{"item": {"name": "  A  film ", "url": " /mediathek/a-film ", "description": "A story"}},
			{"item": {"name": "A film", "url": "/mediathek/a-film"}},
			{"item": {"name": "No link"}}
		]}</script>`

	doc, err := goquery.NewDocumentFromReader(strings.NewReader(page))
	if err != nil {
		t.Fatal(err)
	}

	items := Extract(doc, site, siteURL)

	if len(items) != 1 {
		t.Fatalf("got %d items, want 1: %+v", len(items), items)
	}

	want := Item{Title: "A film", Link: "https://tele5.de/mediathek/a-film", Description: "A story"}
	if items[0].Title != want.Title || items[0].Link != want.Link || items[0].Description != want.Description {
		t.Errorf("got %+v, want %+v", items[0], want)
	}
}
