package scrape

import (
	"bytes"
	"net/url"
	"slices"
	"testing"

	"github.com/PuerkitoBio/goquery"
)

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

func TestSplitSelector(t *testing.T) {
	tests := []struct {
		selector  string
		css, attr string
	}{
		// no pseudo element means the text, ::text spells the same out
		{"h3", "h3", ""},
		{"h3::text", "h3", ""},
		{"a", "a", ""},
		// an attribute has to be asked for
		{"a::attr(href)", "a", "href"},
		{"div::attr(data-url)", "div", "data-url"},
		{"time::attr(datetime)", "time", "datetime"},
		// no css part means the matched item itself
		{"::attr(data-url)", "", "data-url"},
		{"::text", "", ""},
		// css that contains an "@" or brackets stays in one piece
		{"a[href*='@']", "a[href*='@']", ""},
		{"a:not([href*='@'])::attr(href)", "a:not([href*='@'])", "href"},
		{"", "", ""},
	}

	for _, tt := range tests {
		css, attr := splitSelector(tt.selector)
		if css != tt.css || attr != tt.attr {
			t.Errorf("splitSelector(%q) = (%q, %q), want (%q, %q)", tt.selector, css, attr, tt.css, tt.attr)
		}
	}
}

func TestValues(t *testing.T) {
	doc, err := goquery.NewDocumentFromReader(bytes.NewReader([]byte(`
		<div class="card" data-url="/on-the-item">
			<a href="/first">one</a>
			<a>no href</a>
			<a href="mailto:kontakt@example.com">mail</a>
			<h3>Title</h3>
			<h3>Title</h3>
			<time datetime="2026-01-01">1. Januar</time>
		</div>`)))
	if err != nil {
		t.Fatal(err)
	}

	item := doc.Find("div.card")

	firstTests := map[string]string{
		"h3":                    "Title", // the duplicated title must not become "TitleTitle"
		"h3::text":              "Title",
		"a::attr(href)":         "/first",
		"time::attr(datetime)":  "2026-01-01",
		"::attr(data-url)":      "/on-the-item",
		"":                      "", // an unset field stays empty
		".missing":              "",
		"a::attr(data-missing)": "",
	}

	for selector, want := range firstTests {
		if got := value(item, selector); got != want {
			t.Errorf("value(%q) = %q, want %q", selector, got, want)
		}
	}

	allTests := map[string][]string{
		// the anchor without an href is skipped, and an "@" is plain css
		"a::attr(href)":                  {"/first", "mailto:kontakt@example.com"},
		"a[href*='@']::attr(href)":       {"mailto:kontakt@example.com"},
		"a:not([href*='@'])::attr(href)": {"/first"},
		"a::text":                        {"one", "no href", "mail"},
		"a":                              {"one", "no href", "mail"},
		"h3::attr(href)":                 nil, // no such attribute to read
		"":                               nil,
	}

	for selector, want := range allTests {
		if got := values(item, selector); !slices.Equal(got, want) {
			t.Errorf("values(%q) = %q, want %q", selector, got, want)
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
