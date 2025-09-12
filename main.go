package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"

	"github.com/VictoriaMetrics/metrics"
	"github.com/goccy/go-yaml"
	"github.com/gorilla/feeds"
	"golang.org/x/sync/errgroup"
)

type Config struct {
	Sites []Site `yaml:"sites"`
}

type Site struct {
	Name             string `yaml:"name"`
	Title            string `yaml:"title"`
	SiteDescription  string `yaml:"siteDescription"`
	URL              string `yaml:"url"`
	ItemStart        string `yaml:"itemStart"`
	ItemEnd          string `yaml:"itemEnd"`
	LinkStart        string `yaml:"linkStart"`
	LinkEnd          string `yaml:"linkEnd"`
	TitleStart       string `yaml:"titleStart"`
	TitleEnd         string `yaml:"titleEnd"`
	DescriptionStart string `yaml:"descriptionStart"`
	DescriptionEnd   string `yaml:"descriptionEnd"`
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
				count := updateCache(site, *cachePath)

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

func updateCache(site Site, cachePath string) uint64 {
	client := http.Client{Timeout: 10 * time.Second}

	resp, err := client.Get(site.URL)
	if err != nil {
		log.Panicln(err)

	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		log.Panicln(err)
	}

	rest := string(body)

	var items []Item
	for {
		regItemStart := regexp.MustCompile(site.ItemStart).FindString(rest)
		if regItemStart == "" {
			break
		}

		_, after, found := strings.Cut(rest, regItemStart)
		if !found {
			break
		}

		regItemEnd := regexp.MustCompile(site.ItemEnd).FindString(after)
		if regItemEnd == "" {
			break
		}

		var itemRaw string
		itemRaw, rest, found = strings.Cut(after, regItemEnd)
		if !found {
			break
		}

		_, linkRaw, found := strings.Cut(itemRaw, site.LinkStart)
		if !found {
			break
		}

		linkRaw, _, found = strings.Cut(linkRaw, site.LinkEnd)
		if !found || linkRaw == "" || linkRaw == "/" {
			continue
		}

		regTitleStart := regexp.MustCompile(site.TitleStart).FindString(itemRaw)
		if regTitleStart == "" {
			continue
		}

		_, titleRaw, found := strings.Cut(itemRaw, regTitleStart)
		if !found {
			continue
		}

		regTitleEnd := regexp.MustCompile(site.TitleEnd).FindString(titleRaw)
		if regTitleEnd == "" {
			continue
		}

		titleRaw, _, found = strings.Cut(titleRaw, regTitleEnd)
		if !found {
			continue
		}

		_, descriptionRaw, found := strings.Cut(itemRaw, regexp.MustCompile(site.DescriptionStart).FindString(itemRaw))
		if !found {
			continue
		}

		descriptionRaw, _, found = strings.Cut(descriptionRaw, regexp.MustCompile(site.DescriptionEnd).FindString(descriptionRaw))
		if !found {
			continue
		}

		siteURL, err := url.Parse(site.URL)
		if err != nil {
			log.Fatal(err)
		}

		link, err := url.JoinPath(siteURL.Host, linkRaw)
		if err != nil {
			log.Panicln()
		}

		link = strings.TrimSpace(link)
		if !strings.HasPrefix(link, "http") {
			link = "https://" + link
		}

		items = append(items, Item{
			Link:        link,
			Title:       strings.TrimSpace(html.UnescapeString(titleRaw)),
			Description: strings.TrimSpace(html.UnescapeString(descriptionRaw)),
			AddedAt:     time.Now().UTC(),
		})
	}

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
		Description: site.SiteDescription,
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

	return uint64(len(items))
}
