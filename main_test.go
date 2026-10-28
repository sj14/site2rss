package main

import (
	"bytes"
	"compress/gzip"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"github.com/PuerkitoBio/goquery"
	"github.com/sj14/site2rss/internal/config"
	"github.com/sj14/site2rss/internal/scrape"
)

// These tests run the shipped config against stored copies of the sites, so a
// selector that stops matching fails here instead of silently emptying a feed.

func loadConfig(t *testing.T) config.Config {
	t.Helper()

	conf, err := config.Load("config.yaml")
	if err != nil {
		t.Fatal(err)
	}

	return conf
}

// readFixture returns the stored copy of a site. The copies are kept gzipped,
// the pages are around 800 KB each uncompressed.
func readFixture(t *testing.T, file string) []byte {
	t.Helper()

	f, err := os.Open(filepath.Join("testdata", file))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	r, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	b, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}

	return b
}

// loadFixture parses the stored copy of an html site.
func loadFixture(t *testing.T, name string) *goquery.Document {
	t.Helper()

	doc, err := goquery.NewDocumentFromReader(bytes.NewReader(readFixture(t, name+".html.gz")))
	if err != nil {
		t.Fatal(err)
	}

	return doc
}

// extractFixture runs the configured extraction over the stored copy of a site.
// A JSON API is stored as the response it sent, everything else as a page.
func extractFixture(t *testing.T, site config.Site, siteURL *url.URL) []scrape.Item {
	t.Helper()

	if site.Format != config.FormatJSON {
		return scrape.Extract(loadFixture(t, site.Name), site, siteURL)
	}

	items, err := scrape.ExtractJSON(readFixture(t, site.Name+".json.gz"), site, siteURL)
	if err != nil {
		t.Fatal(err)
	}

	return items
}

// TestConfiguredSelectors checks what the configured selectors pull out of the
// stored pages. The counts are what the fixtures contained when they were taken;
// a mismatch means either the selector broke or the fixture needs refreshing.
func TestConfiguredSelectors(t *testing.T) {
	// ARD is 69 teasers on the page, minus one duplicate link and minus one film
	// that is listed twice under different slugs. ZDF is 48 tiles, three of which
	// repeat a film that another row already shows. Joyn is the 30 tiles of the
	// rows its HTML carries, minus the films that appear in more than one of them.
	// JustWatch is the films tagged as free plus the free top 10, which carries no
	// tag; rentals and series are left out. Pluto is every film tile on the page,
	// minus the films the hero and several rows share. Tele5 is the 34 entries of
	// its json-ld ItemList, Rakuten the 48 entries the API was asked for.
	want := map[string]int{
		"ARD": 67, "ZDF": 45, "Arte": 10, "Joyn": 27, "JustWatch": 45, "Pluto": 105,
		"Tele5": 34, "Rakuten": 48,
	}

	for _, site := range loadConfig(t).Sites {
		t.Run(site.Name, func(t *testing.T) {
			siteURL, err := url.Parse(site.URL)
			if err != nil {
				t.Fatal(err)
			}

			items := extractFixture(t, site, siteURL)
			if len(items) != want[site.Name] {
				t.Errorf("got %d items, want %d", len(items), want[site.Name])
			}

			for i, item := range items {
				if item.Title == "" {
					t.Errorf("item %d has no title: %+v", i, item)
				}

				parsed, err := url.Parse(item.Link)
				if err != nil {
					t.Errorf("item %d has an unparsable link %q: %v", i, item.Link, err)
					continue
				}

				if !parsed.IsAbs() {
					t.Errorf("item %d has a relative link: %q", i, item.Link)
				}
			}
		})
	}
}

// TestPaginationSelector checks that the pagination of a site still resolves to
// the pages it is supposed to follow.
func TestPaginationSelector(t *testing.T) {
	want := map[string]int{"ARD": 3, "ZDF": 0, "Arte": 9}

	for _, site := range loadConfig(t).Sites {
		// only html pages are searched for pagination, the config rejects it for
		// the other formats
		if site.Format != config.FormatHTML {
			continue
		}

		t.Run(site.Name, func(t *testing.T) {
			siteURL, err := url.Parse(site.URL)
			if err != nil {
				t.Fatal(err)
			}

			urls := scrape.PaginationURLs(loadFixture(t, site.Name), site, siteURL)
			if len(urls) != want[site.Name] {
				t.Errorf("got %d pages, want %d: %q", len(urls), want[site.Name], urls)
			}

			for _, pageURL := range urls {
				if pageURL == site.URL {
					t.Errorf("pagination points back at the site itself: %q", pageURL)
				}
			}
		})
	}
}
