package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/VictoriaMetrics/metrics"
	"github.com/sj14/site2rss/internal/cache"
	"github.com/sj14/site2rss/internal/config"
	"github.com/sj14/site2rss/internal/feed"
	"github.com/sj14/site2rss/internal/scrape"
	"golang.org/x/sync/errgroup"
)

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
		return duration
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

	conf, err := config.Load(*configPath)
	if err != nil {
		slog.Error("failed loading config", "path", *configPath, "err", err)
		os.Exit(1)
	}

	stores := make(map[string]*feed.Store, len(conf.Sites))
	for _, site := range conf.Sites {
		stores[site.Name] = &feed.Store{}
	}

	for _, site := range conf.Sites {
		name := strings.ToLower(site.Name)
		store := stores[site.Name]

		http.HandleFunc("/"+name+"/rss", store.Handler(func(f *feed.Feeds) string { return f.RSS }))
		http.HandleFunc("/"+name+"/atom", store.Handler(func(f *feed.Feeds) string { return f.Atom }))
		http.HandleFunc("/"+name+"/json", store.Handler(func(f *feed.Feeds) string { return f.JSON }))
	}

	http.HandleFunc("/metrics", func(w http.ResponseWriter, req *http.Request) {
		metrics.WritePrometheus(w, true)
	})

	ctx, cancel := context.WithCancel(context.Background())

	go func() {
		c := make(chan os.Signal, 1)
		signal.Notify(c, os.Interrupt, syscall.SIGTERM)

		<-c
		cancel()
	}()

	go func() {
		for {
			for _, site := range conf.Sites {
				count, err := updateSite(site, *cachePath, stores[site.Name])
				if err != nil {
					slog.Error("failed updating site", "site", site.Name, "err", err)
					// do not continue the loop to update the metrics below
				}

				metrics.GetOrCreateGauge(fmt.Sprintf(`item_size{name=%q}`, site.Name), nil).Set(float64(count))
				slog.Info("updated", "site", site.Name, "items", count)
			}

			time.Sleep(*updateInterval)
		}
	}()

	srv := http.Server{
		Addr: *addr,
	}

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

// updateSite refreshes one site: collect what it lists now, keep the timestamps
// of what was already known, persist that and publish the rendered feeds.
func updateSite(site config.Site, cachePath string, store *feed.Store) (uint64, error) {
	items, err := scrape.Collect(site)
	if err != nil {
		return 0, err
	}

	cached, err := cache.Load(cachePath, site.Name)
	if err != nil {
		return 0, err
	}

	items = cache.Merge(items, cached)

	for _, item := range items {
		slog.Info("found", "site", site.Name, "title", item.Title, "description", item.Description, "link", item.Link)
	}

	if err := cache.Save(cachePath, site.Name, items); err != nil {
		return 0, err
	}

	rendered, err := feed.Render(site, items)
	if err != nil {
		return 0, err
	}

	store.Publish(rendered)

	return uint64(len(items)), nil
}
