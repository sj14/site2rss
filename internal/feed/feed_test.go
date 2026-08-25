package feed

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/sj14/site2rss/internal/config"
	"github.com/sj14/site2rss/internal/scrape"
)

func TestStore(t *testing.T) {
	store := &Store{}
	srv := httptest.NewServer(store.RSS())
	defer srv.Close()

	// nothing rendered yet
	resp, err := http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()

	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Errorf("got %d before the first update, want 503", resp.StatusCode)
	}

	site := config.Site{Name: "Test", Title: "Test Feed", URL: "https://example.com"}
	rendered, err := Render(site, []scrape.Item{{
		Title:   "Film",
		Link:    "https://example.com/film",
		AddedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}})
	if err != nil {
		t.Fatal(err)
	}

	store.Publish(rendered)

	resp, err = http.Get(srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("got %d after publishing, want 200", resp.StatusCode)
	}

	if got := resp.Header.Get("Content-Type"); got != "application/rss+xml; charset=utf-8" {
		t.Errorf("Content-Type = %q", got)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}

	for _, want := range []string{"<rss", "Test Feed", "https://example.com/film", "</rss>"} {
		if !strings.Contains(string(body), want) {
			t.Errorf("feed does not contain %q", want)
		}
	}
}
