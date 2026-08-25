package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"html"
	"io"
	"io/fs"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/VictoriaMetrics/metrics"
	"github.com/goccy/go-yaml"
	"github.com/gorilla/feeds"
	"golang.org/x/sync/errgroup"
)

type Config struct {
	Sites []Site `yaml:"sites"`
}

type Site struct {
	Name        string   `yaml:"name"`
	Title       string   `yaml:"title"`
	Description string   `yaml:"description"`
	URL         string   `yaml:"url"`
	Selector    Selector `yaml:"selector"`
}

type Selector struct {
	Item        string `yaml:"item"`
	Link        string `yaml:"link"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	Pagination  string `yaml:"pagination"`
}

type Item struct {
	Title       string
	Link        string
	Description string
	AddedAt     time.Time
}

func lookupEnvString(key string, defaultVal string) string {
	if val, ok := os.LookupEnv(key); ok {
		return val
	}
	return defaultVal
}

func lookupEnvDuration(key string, defaultVal time.Duration) time.Duration {
	if val, ok := os.LookupEnv(key); ok {
		duration, err := time.ParseDuration(val)
		if err != nil {
			slog.Error("failed parsing duration", "value", val, "key", key, "err", err)
			os.Exit(1)
		}
		return time.Duration(duration)
	}
	return defaultVal
}

func main() {
	var (
		configPath     = flag.String("config", lookupEnvString("CONFIG", "config.yaml"), "path to the config file")
		cachePath      = flag.String("cache", lookupEnvString("CACHE", "cache"), "path to the cache dir")
		updateInterval = flag.Duration("interval", lookupEnvDuration("INTERVAL", 1*time.Hour), "update interval")
		addr           = flag.String("listen", lookupEnvString("LISTEN", ":8080"), "listen address")
	)
	flag.Parse()

	confBytes, err := os.ReadFile(*configPath)
	if err != nil {
		slog.Error("failed reading config", "path", *configPath, "err", err)
		os.Exit(1)
	}

	var config Config
	decoder := yaml.NewDecoder(bytes.NewReader(confBytes), yaml.DisallowUnknownField())

	err = decoder.Decode(&config)
	if err != nil {
		slog.Error("failed decoding config", "path", *configPath, "err", err)
		os.Exit(1)
	}

	for _, site := range config.Sites {
		feedStore[strings.ToLower(site.Name)] = &atomic.Pointer[renderedFeeds]{}
	}

	go func() {
		for {
			updates := make(map[string]uint64, len(config.Sites))

			for _, site := range config.Sites {
				count, err := updateCache(site, *cachePath)
				if err != nil {
					slog.Error("failed updating cache", "site", site.Name, "err", err)
					// do not continue the loop to update the metrics below
				}

				updates[site.Name] = count
				if itemSizesMetrics[site.Name] == nil {
					itemSizesMetrics[site.Name] = metrics.NewGauge(fmt.Sprintf(`item_size{name="%s"}`, site.Name), nil)
				}
				itemSizesMetrics[site.Name].Set(float64(count))
			}

			for site, updated := range updates {
				slog.Info("updates", site, updated)
			}

			time.Sleep(*updateInterval)
		}
	}()

	for _, site := range config.Sites {
		name := strings.ToLower(site.Name)
		store := feedStore[name]

		http.HandleFunc("/"+name+"/rss", serveFeed(store, func(f *renderedFeeds) string { return f.rss }))
		http.HandleFunc("/"+name+"/atom", serveFeed(store, func(f *renderedFeeds) string { return f.atom }))
		http.HandleFunc("/"+name+"/json", serveFeed(store, func(f *renderedFeeds) string { return f.json }))
	}

	http.HandleFunc("/metrics", func(w http.ResponseWriter, req *http.Request) {
		metrics.WritePrometheus(w, true)
	})

	srv := http.Server{
		Addr: *addr,
	}

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)

		<-c
		cancel()
	}()

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		slog.Info("listening", "addr", *addr)
		return srv.ListenAndServe()
	})

	g.Go(func() error {
		<-ctx.Done()
		slog.Info("shutting down")
		ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
		defer cancel()
		return srv.Shutdown(ctx)
	})

	if err := g.Wait(); err != nil {
		slog.Info("exit", "reason", err)
	}

	slog.Info("shut down")
}

// renderedFeeds holds the formats of one site as they are served. It is swapped
// as a whole, so a reader never sees a half updated set of formats.
type renderedFeeds struct {
	rss  string
	atom string
	json string
}

var (
	itemSizesMetrics = map[string]*metrics.Gauge{}
	// feedStore is filled once at startup and only read afterwards. Only the
	// pointers inside are swapped, which keeps the handlers lock free.
	feedStore = map[string]*atomic.Pointer[renderedFeeds]{}
)

// publish makes the rendered feeds visible to the handlers. Sites that were not
// registered at startup, as in tests, have nothing to publish to.
func publish(name string, feeds *renderedFeeds) {
	if store, ok := feedStore[strings.ToLower(name)]; ok {
		store.Store(feeds)
	}
}

// serveFeed writes one format of the current snapshot.
func serveFeed(store *atomic.Pointer[renderedFeeds], pick func(*renderedFeeds) string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		feeds := store.Load()
		if feeds == nil {
			http.Error(w, "feed not generated yet", http.StatusServiceUnavailable)
			return
		}

		io.WriteString(w, pick(feeds))
	}
}

func updateCache(site Site, cachePath string) (uint64, error) {
	siteURL, err := url.Parse(site.URL)
	if err != nil {
		return 0, fmt.Errorf("failed to parse URL %q: %w", site.URL, err)
	}

	doc, err := fetchDocument(site.URL)
	if err != nil {
		return 0, err
	}

	items := appendItems(nil, doc, site, siteURL)

	for _, pageURL := range paginationURLs(doc, site, siteURL) {
		// arte answers with 429 when the pages are requested too fast
		time.Sleep(paginationDelay)

		pageDoc, err := fetchDocument(pageURL)
		if err != nil {
			// keep serving the previous feed rather than dropping the items of
			// this page and re-adding them as new on the next run
			return 0, err
		}

		items = appendItems(items, pageDoc, site, siteURL)
	}

	// a missing cache file is the first run for this site, anything else is a
	// real problem and must not be mistaken for "no items known yet"
	var oldEntries []Item

	loaded, err := os.ReadFile(filepath.Join(cachePath, site.Name+".json"))
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, fmt.Errorf("read cache: %w", err)
	}

	if err == nil {
		if err := json.Unmarshal(loaded, &oldEntries); err != nil {
			return 0, fmt.Errorf("decode cache: %w", err)
		}
	}

	for newIdx, new := range items {
		for _, old := range oldEntries {
			if old.Title == new.Title && old.Link == new.Link && old.Description == new.Description {
				items[newIdx].AddedAt = old.AddedAt
			}
		}
	}

	slices.SortStableFunc(items, func(a, b Item) int {
		if a.AddedAt.Equal(b.AddedAt) {
			return 0
		}
		if a.AddedAt.UnixNano() > b.AddedAt.UnixNano() {
			return -1
		}
		return 1
	})

	items = slices.Compact(items)

	for _, item := range items {
		slog.Info("found", "site", site.Name, "title", item.Title, "description", item.Description, "link", item.Link)
	}

	b, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return 0, fmt.Errorf("encode cache: %w", err)
	}

	err = os.WriteFile(filepath.Join(cachePath, site.Name+".json"), b, os.ModePerm)
	if err != nil {
		return 0, fmt.Errorf("write cache: %w", err)
	}

	feed := &feeds.Feed{
		Title:       site.Title,
		Link:        &feeds.Link{Href: site.URL},
		Description: site.Description,
	}

	for _, lt := range items {
		feed.Items = append(feed.Items, &feeds.Item{
			Id:          lt.Link,
			Title:       lt.Title,
			Link:        &feeds.Link{Href: lt.Link},
			Description: lt.Description,
			Created:     lt.AddedAt,
		})
	}

	rendered := &renderedFeeds{}

	rendered.rss, err = feed.ToRss()
	if err != nil {
		return 0, fmt.Errorf("build rss feed: %w", err)
	}

	rendered.atom, err = feed.ToAtom()
	if err != nil {
		return 0, fmt.Errorf("build atom feed: %w", err)
	}

	rendered.json, err = feed.ToJSON()
	if err != nil {
		return 0, fmt.Errorf("build json feed: %w", err)
	}

	publish(site.Name, rendered)

	return uint64(len(items)), nil
}

const (
	// maxPaginationPages caps how many extra pages a single site may pull in, so a
	// too broad pagination selector cannot turn one update into hundreds of requests.
	maxPaginationPages = 10
	// paginationDelay is the pause between two page requests of the same site.
	// arte serves roughly ten requests per minute before it starts answering
	// with 429, so the pages are spread out instead of run back to back.
	paginationDelay = 10 * time.Second
	// fetchAttempts, retryDelay and maxRetryDelay control the backoff once a page
	// is rate limited anyway. retryDelay only applies when the response carries no
	// usable Retry-After header. arte hands out a minute per 429, so the attempts
	// need to cover several of those to keep a busy window from failing the update.
	fetchAttempts = 5
	retryDelay    = 30 * time.Second
	maxRetryDelay = 2 * time.Minute
)

// rateLimitError marks a response the site asked us to repeat later.
type rateLimitError struct {
	url        string
	retryAfter time.Duration
}

func (e *rateLimitError) Error() string {
	return fmt.Sprintf("rate limited for %q, retry after %s", e.url, e.retryAfter)
}

// retryAfter reads how long the site wants us to wait. The header may also hold
// an HTTP date, which falls back to the fixed delay.
func retryAfter(resp *http.Response) time.Duration {
	seconds, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil {
		return retryDelay
	}

	return min(time.Duration(seconds)*time.Second, maxRetryDelay)
}

// fetchDocument loads and parses a single page. Arte answers with 429 when it
// gets several requests in a short time, so a rate limited page is retried after
// the delay the response asks for instead of losing its items.
func fetchDocument(pageURL string) (*goquery.Document, error) {
	client := http.Client{Timeout: 10 * time.Second}

	var err error

	for attempt := 1; attempt <= fetchAttempts; attempt++ {
		var doc *goquery.Document

		doc, err = fetchOnce(&client, pageURL)
		if err == nil {
			return doc, nil
		}

		var limited *rateLimitError
		if !errors.As(err, &limited) {
			return nil, err
		}

		if attempt < fetchAttempts {
			slog.Warn("rate limited, retrying", "url", pageURL, "delay", limited.retryAfter)
			time.Sleep(limited.retryAfter)
		}
	}

	return nil, err
}

func fetchOnce(client *http.Client, pageURL string) (*goquery.Document, error) {
	resp, err := client.Get(pageURL)
	if err != nil {
		return nil, fmt.Errorf("failed loading site (%q): %w", pageURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &rateLimitError{url: pageURL, retryAfter: retryAfter(resp)}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("non 200 status (%d) for %q", resp.StatusCode, pageURL)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse document (%q): %w", pageURL, err)
	}

	return doc, nil
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

// paginationURLs resolves the links the pagination selector matches on the given
// document. The links are only followed one level deep, so the pages behind a
// "show more" button are picked up without crawling the whole site.
func paginationURLs(doc *goquery.Document, site Site, siteURL *url.URL) []string {
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

// appendItems extracts the items of a single page, skipping the ones already
// collected from previous pages.
func appendItems(items []Item, doc *goquery.Document, site Site, siteURL *url.URL) []Item {
	doc.Find(site.Selector.Item).Each(func(i int, s *goquery.Selection) {
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

// normalizeSpace trims the value and collapses runs of whitespace into a single
// space, since the markup indentation ends up inside the extracted text.
func normalizeSpace(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// getFields returns one value per element the selector matches. A selector of the
// form "attr@css" reads that attribute, otherwise the element text is used.
// Elements without the attribute are skipped.
func getFields(s *goquery.Selection, selector string) []string {
	attr, css, isAttr := strings.Cut(selector, "@")
	if !isAttr {
		css = selector
	}

	var values []string

	s.Find(css).Each(func(i int, el *goquery.Selection) {
		if !isAttr {
			values = append(values, el.Text())
			return
		}

		if val, ok := el.Attr(attr); ok {
			values = append(values, val)
		}
	})

	return values
}

// getField returns the first value the selector matches, or an empty string.
// Taking a single element is what keeps a teaser that carries its title twice
// (arte does) from ending up as "TitleTitle".
func getField(s *goquery.Selection, selector string) string {
	values := getFields(s, selector)
	if len(values) == 0 {
		return ""
	}

	return values[0]
}
