package main

import (
	"compress/gzip"
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

// loadFixture parses the stored copy of a site. The pages are kept gzipped, they
// are around 800 KB each uncompressed.
func loadFixture(t *testing.T, name string) *goquery.Document {
	t.Helper()

	f, err := os.Open(filepath.Join("testdata", name+".html.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	r, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	doc, err := goquery.NewDocumentFromReader(r)
	if err != nil {
		t.Fatal(err)
	}

	return doc
}

// TestConfiguredSelectors checks what the configured selectors pull out of the
// stored pages. The counts are what the fixtures contained when they were taken;
// a mismatch means either the selector broke or the fixture needs refreshing.
func TestConfiguredSelectors(t *testing.T) {
	// ARD is 69 teasers on the page, minus one duplicate link and minus one film
	// that is listed twice under different slugs. ZDF is 48 tiles, three of which
	// repeat a film that another row already shows.
	want := map[string]int{"ARD": 67, "ZDF": 45, "Arte": 10}

	for _, site := range loadConfig(t).Sites {
		t.Run(site.Name, func(t *testing.T) {
			siteURL, err := url.Parse(site.URL)
			if err != nil {
				t.Fatal(err)
			}

			items := scrape.Extract(loadFixture(t, site.Name), site, siteURL)
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
