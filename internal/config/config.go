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

// Site is one source page together with the feed metadata generated from it.
type Site struct {
	Name        string   `yaml:"name"`
	Title       string   `yaml:"title"`
	Description string   `yaml:"description"`
	URL         string   `yaml:"url"`
	Selector    Selector `yaml:"selector"`
}

// Selector describes how the items and their fields are picked out of the page.
// A value of the form "attr@css" reads that attribute of the matched element,
// anything else is read as its text. Pagination is optional and names the links
// to further pages of the same listing.
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

	return config, nil
}
