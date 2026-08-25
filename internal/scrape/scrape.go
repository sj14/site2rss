// Package scrape turns a configured site into the items of its feed.
package scrape

import (
	"context"
	"fmt"
	"html"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/sj14/site2rss/internal/config"
)

const (
	// maxPaginationPages caps how many extra pages a single site may pull in, so a
	// too broad pagination selector cannot turn one update into hundreds of requests.
	maxPaginationPages = 10
	// paginationDelay is the pause between two page requests of the same site.
	// arte serves roughly ten requests per minute before it starts answering
	// with 429, so the pages are spread out instead of run back to back.
	paginationDelay = 10 * time.Second
)

// Item is a single entry of a feed.
type Item struct {
	Title       string
	Link        string
	Description string
	AddedAt     time.Time
}

// Collect loads the site and every page its pagination points at, and returns
// the items found on them.
func Collect(ctx context.Context, site config.Site) ([]Item, error) {
	siteURL, err := url.Parse(site.URL)
	if err != nil {
		return nil, fmt.Errorf("parse URL %q: %w", site.URL, err)
	}

	doc, err := fetchDocument(ctx, site.URL)
	if err != nil {
		return nil, err
	}

	items := appendItems(nil, doc, site, siteURL)

	for _, pageURL := range PaginationURLs(doc, site, siteURL) {
		// arte answers with 429 when the pages are requested too fast
		if err := sleep(ctx, paginationDelay); err != nil {
			return nil, err
		}

		pageDoc, err := fetchDocument(ctx, pageURL)
		if err != nil {
			// keep serving the previous feed rather than dropping the items of
			// this page and re-adding them as new on the next run
			return nil, err
		}

		items = appendItems(items, pageDoc, site, siteURL)
	}

	return items, nil
}

// Extract returns the items of a single already loaded page.
func Extract(doc *goquery.Document, site config.Site, siteURL *url.URL) []Item {
	return appendItems(nil, doc, site, siteURL)
}

// appendItems extracts the items of a single page, skipping the ones already
// collected from previous pages.
func appendItems(items []Item, doc *goquery.Document, site config.Site, siteURL *url.URL) []Item {
	doc.Find(site.Selector.Item).Each(func(_ int, s *goquery.Selection) {
		var (
			title       = normalizeSpace(html.UnescapeString(getField(s, site.Selector.Title)))
			description = normalizeSpace(html.UnescapeString(getField(s, site.Selector.Description)))
		)

		link, err := resolveURL(siteURL, getField(s, site.Selector.Link))
		if err != nil {
			slog.Warn("failed resolving item URL", "site", site.Name, "err", err)
			return
		}

		if isDuplicate(items, title, link) {
			return
		}

		items = append(items, Item{
			Link:        link,
			Title:       title,
			Description: description,
			AddedAt:     time.Now().UTC(),
		})
	})

	return items
}

// isDuplicate reports whether the item was already collected. Beside the link,
// the title is compared as well: the ard lists the same film under several slugs
// when it appears in more than one genre, which the link alone does not catch.
// Items without a title are only deduplicated by their link.
func isDuplicate(items []Item, title, link string) bool {
	for _, item := range items {
		if item.Link == link {
			return true
		}

		if title != "" && item.Title == title {
			return true
		}
	}

	return false
}

// PaginationURLs resolves the links the pagination selector matches on the given
// document. The links are only followed one level deep, so the pages behind a
// "show more" button are picked up without crawling the whole site.
func PaginationURLs(doc *goquery.Document, site config.Site, siteURL *url.URL) []string {
	if site.Selector.Pagination == "" {
		return nil
	}

	var (
		urls []string
		seen = map[string]bool{siteURL.String(): true}
	)

	for _, href := range getFields(doc.Selection, site.Selector.Pagination) {
		pageURL, err := resolveURL(siteURL, href)
		if err != nil {
			slog.Warn("failed resolving pagination URL", "site", site.Name, "err", err)
			continue
		}

		if seen[pageURL] {
			continue
		}
		seen[pageURL] = true

		urls = append(urls, pageURL)
	}

	if len(urls) > maxPaginationPages {
		slog.Warn("dropping pagination pages beyond the limit", "site", site.Name, "found", len(urls), "limit", maxPaginationPages)
		urls = urls[:maxPaginationPages]
	}

	return urls
}

// resolveURL turns a href found on a page into an absolute URL, following the
// same rule a browser applies: relative paths, absolute URLs and protocol
// relative links all resolve against the page they were found on.
func resolveURL(base *url.URL, href string) (string, error) {
	ref, err := url.Parse(strings.TrimSpace(href))
	if err != nil {
		return "", fmt.Errorf("parse URL %q: %w", href, err)
	}

	return base.ResolveReference(ref).String(), nil
}
