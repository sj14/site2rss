package scrape

import (
	"bytes"
	"net/url"
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
