package main

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"flag"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/gorilla/feeds"
	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Sites []Site
}

type Site struct {
	Name             string
	Title            string
	SiteDescription  string
	URL              string
	ItemStart        string
	ItemEnd          string
	LinkStart        string
	LinkEnd          string
	TitleStart       string
	TitleEnd         string
	DescriptionStart string
	DescriptionEnd   string
}

type Item struct {
	Title       string
	Link        string
	Description string
	AddedAt     time.Time
}

func main() {
	var (
		configPath     = flag.String("config", "config/config.toml", "path to the config file")
		cachePath      = flag.String("cache", "cache", "path to the cache dir")
		updateInterval = flag.Duration("interval", 1*time.Hour, "update interval")
		addr           = flag.String("address", ":8080", "listen address")
	)
	flag.Parse()

	confBytes, err := os.ReadFile(*configPath)
	if err != nil {
		log.Fatalln(err)
	}

	var config Config
	decoder := toml.NewDecoder(bytes.NewReader(confBytes))
	err = decoder.DisallowUnknownFields().Decode(&config)
	if err != nil {
		log.Fatalln(err)
	}

	go func() {
		for {
			for _, site := range config.Sites {
				updateCache(site, *cachePath)
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

	err = http.ListenAndServe(*addr, nil)
	if err != nil {
		log.Fatal(err)
	}
}

var state = map[string]string{}

func updateCache(site Site, cachePath string) {
	resp, err := http.Get(site.URL)
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
		_, after, found := strings.Cut(rest, site.ItemStart)
		if !found {
			break
		}

		var itemRaw string
		itemRaw, rest, found = strings.Cut(after, site.ItemEnd)
		if !found {
			break
		}

		_, linkRaw, found := strings.Cut(itemRaw, site.LinkStart)
		if !found {
			break
		}

		linkRaw, _, found = strings.Cut(linkRaw, site.LinkEnd)
		if !found {
			continue
		}

		_, titleRaw, found := strings.Cut(itemRaw, site.TitleStart)
		if !found {
			continue
		}

		titleRaw, _, found = strings.Cut(titleRaw, site.TitleEnd)
		if !found {
			continue
		}

		_, descriptionRaw, found := strings.Cut(itemRaw, site.DescriptionStart)
		if !found {
			continue
		}

		descriptionRaw, _, found = strings.Cut(descriptionRaw, site.DescriptionEnd)
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
			Title:       strings.TrimSpace(titleRaw),
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
		if a.AddedAt == b.AddedAt {
			return 0
		}
		if a.AddedAt.UnixNano() > b.AddedAt.UnixNano() {
			return -1
		}
		return 1
	})

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
		id := fmt.Sprintf("%x", md5.Sum(slices.Concat([]byte(lt.Title), []byte(lt.AddedAt.String()))))
		feed.Items = append(feed.Items, &feeds.Item{
			Id:          id,
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
}
