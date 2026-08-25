// Package feed renders the items of a site and serves them.
package feed

import (
	"fmt"
	"io"
	"net/http"
	"sync/atomic"

	"github.com/gorilla/feeds"
	"github.com/sj14/site2rss/internal/config"
	"github.com/sj14/site2rss/internal/scrape"
)

// Feeds holds the formats of one site as they are served.
type Feeds struct {
	RSS  string
	Atom string
	JSON string
}

// Render builds all three formats at once, so a site is either published
// completely or not at all.
func Render(site config.Site, items []scrape.Item) (*Feeds, error) {
	feed := &feeds.Feed{
		Title:       site.Title,
		Link:        &feeds.Link{Href: site.URL},
		Description: site.Description,
	}

	for _, item := range items {
		feed.Items = append(feed.Items, &feeds.Item{
			Id:          item.Link,
			Title:       item.Title,
			Link:        &feeds.Link{Href: item.Link},
			Description: item.Description,
			Created:     item.AddedAt,
		})
	}

	rendered := &Feeds{}

	var err error
	if rendered.RSS, err = feed.ToRss(); err != nil {
		return nil, fmt.Errorf("build rss feed: %w", err)
	}

	if rendered.Atom, err = feed.ToAtom(); err != nil {
		return nil, fmt.Errorf("build atom feed: %w", err)
	}

	if rendered.JSON, err = feed.ToJSON(); err != nil {
		return nil, fmt.Errorf("build json feed: %w", err)
	}

	return rendered, nil
}

// Store holds the feeds of one site. The whole set is swapped at once, so a
// reader never sees a half updated site and no lock is needed to read.
type Store struct {
	current atomic.Pointer[Feeds]
}

func (s *Store) Publish(rendered *Feeds) {
	s.current.Store(rendered)
}

func (s *Store) RSS() http.HandlerFunc {
	return s.handler("application/rss+xml; charset=utf-8", func(f *Feeds) string { return f.RSS })
}

func (s *Store) Atom() http.HandlerFunc {
	return s.handler("application/atom+xml; charset=utf-8", func(f *Feeds) string { return f.Atom })
}

func (s *Store) JSON() http.HandlerFunc {
	return s.handler("application/feed+json; charset=utf-8", func(f *Feeds) string { return f.JSON })
}

// handler serves one format of the current snapshot.
func (s *Store) handler(contentType string, pick func(*Feeds) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rendered := s.current.Load()
		if rendered == nil {
			http.Error(w, "feed not generated yet", http.StatusServiceUnavailable)
			return
		}

		w.Header().Set("Content-Type", contentType)
		io.WriteString(w, pick(rendered))
	}
}
