package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
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
			log.Fatalf("failed parsing %q as duration (%q): %v", val, key, err)
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
		log.Fatalln(err)
	}

	var config Config
	decoder := yaml.NewDecoder(bytes.NewReader(confBytes), yaml.DisallowUnknownField())

	err = decoder.Decode(&config)
	if err != nil {
		log.Fatalln(err)
	}

	go func() {
		for {
			updates := make(map[string]uint64, len(config.Sites))

			for _, site := range config.Sites {
				count, err := updateCache(site, *cachePath)
				if err != nil {
					log.Println(err)
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
		http.HandleFunc("/"+strings.ToLower(site.Name)+"/rss", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(state[strings.ToLower(site.Name)+"_rss"]))
		})
		http.HandleFunc("/"+strings.ToLower(site.Name)+"/atom", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(state[strings.ToLower(site.Name)+"_atom"]))
		})
		http.HandleFunc("/"+strings.ToLower(site.Name)+"/json", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(state[strings.ToLower(site.Name)+"_json"]))
		})
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

var (
	itemSizesMetrics = map[string]*metrics.Gauge{}
	state            = map[string]string{}
)

func updateCache(site Site, cachePath string) (uint64, error) {
	client := http.Client{Timeout: 10 * time.Second}

	siteURL, err := url.Parse(site.URL)
	if err != nil {
		return 0, fmt.Errorf("failed to parse URL %q: %w", site.URL, err)
	}

	resp, err := client.Get(site.URL)
	if err != nil {
		return 0, fmt.Errorf("failed loading site (%q): %w", site.URL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("non 200 status for %q", site.URL)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return 0, fmt.Errorf("parse document: %w", err)
	}

	var items []Item

	doc.Find(site.Selector.Item).Each(func(i int, s *goquery.Selection) {
		var (
			title       = getField(s, site.Selector.Title)
			linkRaw     = getField(s, site.Selector.Link)
			description = getField(s, site.Selector.Description)
		)

		link, err := url.JoinPath(siteURL.Host, linkRaw)
		if err != nil {
			log.Printf("failed to join URL: %v\n", err)
			return
		}

		link = strings.TrimSpace(link)
		if !strings.HasPrefix(link, "http") {
			link = "https://" + link
		}

		for _, item := range items {
			if item.Link == link {
				return
			}
		}

		items = append(items, Item{
			Link:        link,
			Title:       strings.TrimSpace(html.UnescapeString(title)),
			Description: strings.TrimSpace(html.UnescapeString(description)),
			AddedAt:     time.Now().UTC(),
		})
	})

	var oldEntries []Item

	loaded, err := os.ReadFile(filepath.Join(cachePath, site.Name+".json"))
	if err != nil {
		if !strings.Contains(err.Error(), "no such file or directory") &&
			!strings.Contains(err.Error(), "The system cannot find the file specified.") {
			log.Panicln(err)
		}
	} else {
		err = json.Unmarshal(loaded, &oldEntries)
		if err != nil {
			log.Panicln(err)
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
		log.Fatal(err)
	}

	err = os.WriteFile(filepath.Join(cachePath, site.Name+".json"), b, os.ModePerm)
	if err != nil {
		log.Fatal(err)
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

	rss, err := feed.ToRss()
	if err != nil {
		log.Fatal(err)
	}

	state[strings.ToLower(site.Name)+"_rss"] = rss

	atom, err := feed.ToAtom()
	if err != nil {
		log.Fatal(err)
	}

	state[strings.ToLower(site.Name)+"_atom"] = atom

	json, err := feed.ToJSON()
	if err != nil {
		log.Fatal(err)
	}

	state[strings.ToLower(site.Name)+"_json"] = json

	return uint64(len(items)), nil
}

func getField(s *goquery.Selection, selector string) string {
	parts := strings.SplitN(selector, "@", 2)
	el := s.Find(parts[len(parts)-1])
	if len(parts) == 2 {
		val, _ := el.Attr(parts[len(parts)-2])
		return val
	}
	return el.Text()
}
