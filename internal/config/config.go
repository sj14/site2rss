// Package config holds the site definitions site2rss works from.
package config

import (
	"bytes"
	"fmt"
	"os"

	"github.com/goccy/go-yaml"
)

type Config struct {
	Sites []Site `yaml:"sites"`
}

// The formats a source can come in.
const (
	// FormatHTML is the default and reads the page through css selectors.
	FormatHTML = ""
	// FormatJSONLD reads the schema.org ItemList a page embeds for search engines.
	// Its fields are fixed by the standard, so it takes no selectors.
	FormatJSONLD = "json-ld"
	// FormatJSON reads a JSON API through dotted paths, see Selector.
	FormatJSON = "json"
)

// Site is one source page together with the feed metadata generated from it.
type Site struct {
	Name        string   `yaml:"name"`
	Title       string   `yaml:"title"`
	Description string   `yaml:"description"`
	URL         string   `yaml:"url"`
	Format      string   `yaml:"format"`
	Selector    Selector `yaml:"selector"`
}

// Selector describes how the items and their fields are picked out of the page.
// For html they are css selectors, with ::text or ::attr(name) saying what to
// read from a match. Pagination is optional and names the links to further
// pages of the same listing.
//
// For json, Item is the dotted path to the array of entries, Title and
// Description are dotted paths inside one entry, and Link is a URL template
// whose {path} placeholders are filled from the entry.
type Selector struct {
	Item        string `yaml:"item"`
	Link        string `yaml:"link"`
	Title       string `yaml:"title"`
	Description string `yaml:"description"`
	Pagination  string `yaml:"pagination"`
}

// Load reads the config file. Unknown fields are rejected, so a misspelled
// selector name fails loudly instead of silently doing nothing.
func Load(path string) (Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("read config: %w", err)
	}

	var config Config
	if err := yaml.NewDecoder(bytes.NewReader(b), yaml.DisallowUnknownField()).Decode(&config); err != nil {
		return Config{}, fmt.Errorf("decode config: %w", err)
	}

	for _, site := range config.Sites {
		if err := site.validate(); err != nil {
			return Config{}, fmt.Errorf("site %q: %w", site.Name, err)
		}
	}

	return config, nil
}

// validate rejects formats site2rss does not know, and pagination where it would
// silently do nothing: only html pages are searched for links to further pages.
func (s Site) validate() error {
	switch s.Format {
	case FormatHTML, FormatJSONLD, FormatJSON:
	default:
		return fmt.Errorf("unknown format %q", s.Format)
	}

	if s.Format != FormatHTML && s.Selector.Pagination != "" {
		return fmt.Errorf("pagination is not supported for format %q", s.Format)
	}

	return nil
}
