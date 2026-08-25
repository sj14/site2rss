package main

import (
	"bytes"
	"compress/gzip"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/goccy/go-yaml"
)

// loadConfig reads the config the binary ships with, so a selector that stops
// matching fails the tests instead of silently emptying a feed.
func loadConfig(t *testing.T) Config {
	t.Helper()

	b, err := os.ReadFile("config.yaml")
	if err != nil {
		t.Fatal(err)
	}

	var config Config
	if err := yaml.NewDecoder(bytes.NewReader(b), yaml.DisallowUnknownField()).Decode(&config); err != nil {
		t.Fatal(err)
	}

	return config
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

// TestConfiguredSelectors runs the configured selectors against the stored pages.
// The counts are what the fixtures contained when they were taken; a mismatch
// means either the selector broke or the fixture needs refreshing.
func TestConfiguredSelectors(t *testing.T) {
	// ARD is 69 teasers on the page, minus one duplicate link and minus one film
	// that is listed twice under different slugs
	want := map[string]int{"ARD": 67, "ZDF": 49, "Arte": 10}

	for _, site := range loadConfig(t).Sites {
		t.Run(site.Name, func(t *testing.T) {
			siteURL, err := url.Parse(site.URL)
			if err != nil {
				t.Fatal(err)
			}

			items := appendItems(nil, loadFixture(t, site.Name), site, siteURL)
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

			urls := paginationURLs(loadFixture(t, site.Name), site, siteURL)
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

func TestResolveURL(t *testing.T) {
	base, err := url.Parse("https://www.ardmediathek.de/filme")
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		href string
		want string
	}{
		{"/video/x", "https://www.ardmediathek.de/video/x"},
		{"/video/x?isChildContent", "https://www.ardmediathek.de/video/x?isChildContent"},
		{"  /video/x  ", "https://www.ardmediathek.de/video/x"},
		{"https://www.arte.tv/de/videos/1/", "https://www.arte.tv/de/videos/1/"},
		{"//cdn.example.com/x", "https://cdn.example.com/x"},
		{"video/x", "https://www.ardmediathek.de/video/x"},
	}

	for _, tt := range tests {
		got, err := resolveURL(base, tt.href)
		if err != nil {
			t.Errorf("resolveURL(%q): %v", tt.href, err)
			continue
		}

		if got != tt.want {
			t.Errorf("resolveURL(%q) = %q, want %q", tt.href, got, tt.want)
		}
	}
}

func TestMergeWithCache(t *testing.T) {
	var (
		old   = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		newer = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	)

	cached := []Item{
		{Title: "Alt", Link: "https://example.com/alt", AddedAt: old},
		{Title: "Umbenannt", Link: "https://example.com/umbenannt", AddedAt: old},
	}

	items := []Item{
		{Title: "Alt", Link: "https://example.com/alt", AddedAt: newer},
		{Title: "Umbenannt, jetzt anders", Link: "https://example.com/umbenannt", AddedAt: newer},
		{Title: "Neu", Link: "https://example.com/neu", AddedAt: newer},
	}

	got := mergeWithCache(items, cached)

	if len(got) != 3 {
		t.Fatalf("got %d items, want 3", len(got))
	}

	// newest first, so the genuinely new item leads
	if got[0].Link != "https://example.com/neu" {
		t.Errorf("first item is %q, want the new one", got[0].Link)
	}

	for _, item := range got[1:] {
		if !item.AddedAt.Equal(old) {
			t.Errorf("%q lost its timestamp: %s", item.Link, item.AddedAt)
		}
	}
}

func TestGetField(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader([]byte(`
		<div class="item">
			<a href="/first">one</a>
			<a>no href</a>
			<h3>Title</h3>
			<h3>Title</h3>
		</div>`)))
	if err != nil {
		t.Fatal(err)
	}

	item := doc.Find("div.item")

	tests := []struct {
		selector string
		want     string
		wantAll  int
	}{
		{"href@a", "/first", 1}, // elements without the attribute are skipped
		{"h3", "Title", 2},      // the duplicated title must not become "TitleTitle"
		{"", "", 0},             // an unset selector yields nothing
		{".missing", "", 0},     // a selector that matches nothing yields nothing
	}

	for _, tt := range tests {
		if got := getField(item, tt.selector); got != tt.want {
			t.Errorf("getField(%q) = %q, want %q", tt.selector, got, tt.want)
		}

		if got := getFields(item, tt.selector); len(got) != tt.wantAll {
			t.Errorf("getFields(%q) = %q, want %d values", tt.selector, got, tt.wantAll)
		}
	}
}

func TestNormalizeSpace(t *testing.T) {
	tests := map[string]string{
		"  Film  ":                     "Film",
		"Sieben Tote hat die\n\tWoche": "Sieben Tote hat die Woche",
		"":                             "",
		"\n\n":                         "",
	}

	for in, want := range tests {
		if got := normalizeSpace(in); got != want {
			t.Errorf("normalizeSpace(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestIsDuplicate(t *testing.T) {
	items := []Item{
		{Title: "Film", Link: "https://example.com/a"},
		{Title: "", Link: "https://example.com/b"},
	}

	tests := []struct {
		title, link string
		want        bool
	}{
		{"Film", "https://example.com/a", true},   // both match
		{"Anders", "https://example.com/a", true}, // same link under a new title
		{"Film", "https://example.com/c", true},   // same film under another slug
		{"", "https://example.com/c", false},      // untitled items only match by link
		{"Anderer Film", "https://example.com/c", false},
	}

	for _, tt := range tests {
		if got := isDuplicate(items, tt.title, tt.link); got != tt.want {
			t.Errorf("isDuplicate(%q, %q) = %v, want %v", tt.title, tt.link, got, tt.want)
		}
	}
}
