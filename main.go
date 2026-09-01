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
		logLevel       = flag.String("log-level", lookupEnvString("LOG_LEVEL", "info"), "log level (debug, info, warn, error)")
		sourceIP       = flag.String("source-ip", lookupEnvString("SOURCE_IP", ""), "local address to send the scrape requests from")
	)
	flag.Parse()

	// debug logs every extracted item, which is what to turn on when a selector
	// stopped matching
	var level slog.Level
	if err := level.UnmarshalText([]byte(*logLevel)); err != nil {
		slog.Error("failed parsing log level", "value", *logLevel, "err", err)
		os.Exit(1)
	}

	slog.SetLogLoggerLevel(level)

	// sites that serve different content per country go by the address the
	// request comes from, so a host with several addresses can pick one
	if err := scrape.SetSourceIP(*sourceIP); err != nil {
		slog.Error("failed setting source IP", "value", *sourceIP, "err", err)
		os.Exit(1)
	}

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

		http.HandleFunc("/"+name+"/rss", store.RSS())
		http.HandleFunc("/"+name+"/atom", store.Atom())
		http.HandleFunc("/"+name+"/json", store.JSON())
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

	srv := http.Server{
		Addr: *addr,
	}

	g, ctx := errgroup.WithContext(ctx)

	g.Go(func() error {
		updateLoop(ctx, conf, *cachePath, stores, *updateInterval)
		return nil
	})

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

// updateLoop refreshes every site, waits, and starts over until the context is
// cancelled. A site that fails keeps its previous feed and gauge value.
func updateLoop(ctx context.Context, conf config.Config, cachePath string, stores map[string]*feed.Store, interval time.Duration) {
	for {
		for _, site := range conf.Sites {
			count, err := updateSite(ctx, site, cachePath, stores[site.Name])
			if err != nil {
				if ctx.Err() != nil {
					return
				}

				// a failed update is not the same as "this site has no items",
				// so the gauge keeps whatever the last successful run reported
				slog.Error("failed updating site", "site", site.Name, "err", err)
				continue
			}

			metrics.GetOrCreateGauge(fmt.Sprintf(`item_size{name=%q}`, site.Name), nil).Set(float64(count))
			slog.Info("updated", "site", site.Name, "items", count)
		}

		timer := time.NewTimer(interval)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
	}
}

// updateSite refreshes one site: collect what it lists now, keep the timestamps
// of what was already known, persist that and publish the rendered feeds.
func updateSite(ctx context.Context, site config.Site, cachePath string, store *feed.Store) (uint64, error) {
	items, err := scrape.Collect(ctx, site)
	if err != nil {
		return 0, err
	}

	cached, err := cache.Load(cachePath, site.Name)
	if err != nil {
		return 0, err
	}

	items = cache.Merge(items, cached)

	for _, item := range items {
		slog.Debug("found", "site", site.Name, "title", item.Title, "description", item.Description, "link", item.Link)
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
