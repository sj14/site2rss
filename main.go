package main

import (
	"bytes"
	"crypto/md5"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"slices"

	"github.com/gorilla/feeds"
	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	Sites []Site
}

type Site struct {
	Name        string
	Title       string
	Description string
	URL         string
}

type LinkTitle struct {
	Title   string
	Link    string
	AddedAt time.Time
}

func main() {
	var (
		configPath = flag.String("config", "config/config.toml", "path to the config file")
		cachePath  = flag.String("cache", "cache", "path to the cache dir")
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
		for _, site := range config.Sites {
			handleSite(site, *cachePath)
		}
		time.Sleep(1 * time.Hour)
	}()

	err = http.ListenAndServe(":8080", nil)
	if err != nil {
		log.Fatal(err)
	}
}

var state = map[string]string{}

func handleSite(site Site, cachePath string) {
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

	var linksAndTitles []LinkTitle
	for {
		_, after, found := strings.Cut(rest, "<a href=") // TODO
		if !found {
			break
		}

		var linkTitleRaw string
		linkTitleRaw, rest, found = strings.Cut(after, "class=\"teaser-title-link m-clickarea-action js-track-click\"") // TODO
		if !found {
			break
		}

		siteURL, err := url.Parse(site.URL)
		if err != nil {
			log.Fatal(err)
		}

		linksAndTitles = append(linksAndTitles, toLinkTitle(linkTitleRaw, siteURL.Host))
	}

	var oldEntries []LinkTitle

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

	for newIdx, new := range linksAndTitles {
		for _, old := range oldEntries {
			if old.Title == new.Title && old.Link == new.Link {
				linksAndTitles[newIdx].AddedAt = old.AddedAt
			}
		}
	}

	b, err := json.MarshalIndent(linksAndTitles, "", "  ")
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

	for _, lt := range linksAndTitles {
		id := fmt.Sprintf("%x", md5.Sum(slices.Concat([]byte(lt.Title), []byte(lt.AddedAt.String()))))
		feed.Items = append(feed.Items, &feeds.Item{
			Id:      id,
			Title:   lt.Title,
			Link:    &feeds.Link{Href: lt.Link},
			Created: lt.AddedAt,
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

	// TODO: only needs to be registered once
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

func toLinkTitle(s string, linkPrefix string) LinkTitle {
	s = strings.ReplaceAll(s, "\"", "")
	s = strings.ReplaceAll(s, "title=", "")

	split := strings.SplitN(s, " ", 2)
	if len(split) != 2 {
		log.Panicln()
	}

	link, err := url.JoinPath(linkPrefix, split[0])
	if err != nil {
		log.Panicln()
	}
	return LinkTitle{
		Link:    strings.TrimSpace(link),
		Title:   strings.TrimSpace(split[1]),
		AddedAt: time.Now().UTC(),
	}
}
