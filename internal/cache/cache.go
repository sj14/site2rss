// Package cache keeps the items of a site between runs. Without it every restart
// would look like every item had just appeared.
package cache

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/sj14/site2rss/internal/scrape"
)

// Load reads the items stored by the previous run. A missing file is the first
// run for this site, anything else is a real problem and must not be mistaken
// for "nothing known yet".
func Load(dir, name string) ([]scrape.Item, error) {
	loaded, err := os.ReadFile(filepath.Join(dir, name+".json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}

	if err != nil {
		return nil, fmt.Errorf("read cache: %w", err)
	}

	var items []scrape.Item
	if err := json.Unmarshal(loaded, &items); err != nil {
		return nil, fmt.Errorf("decode cache: %w", err)
	}

	return items, nil
}

// Save replaces the stored items. The new content goes to a temporary file that
// is renamed into place, so a process killed mid write cannot leave a truncated
// file behind that the next run would refuse to decode.
func Save(dir, name string, items []scrape.Item) error {
	b, err := json.MarshalIndent(items, "", "  ")
	if err != nil {
		return fmt.Errorf("encode cache: %w", err)
	}

	tmp, err := os.CreateTemp(dir, name+".*.tmp")
	if err != nil {
		return fmt.Errorf("create cache: %w", err)
	}
	defer os.Remove(tmp.Name())

	if _, err := tmp.Write(b); err != nil {
		tmp.Close()
		return fmt.Errorf("write cache: %w", err)
	}

	if err := tmp.Close(); err != nil {
		return fmt.Errorf("close cache: %w", err)
	}

	// CreateTemp uses 0600, the cache is not secret
	if err := os.Chmod(tmp.Name(), 0o644); err != nil {
		return fmt.Errorf("chmod cache: %w", err)
	}

	if err := os.Rename(tmp.Name(), filepath.Join(dir, name+".json")); err != nil {
		return fmt.Errorf("replace cache: %w", err)
	}

	return nil
}

// Merge carries the timestamp of already known items over, so only genuinely new
// entries move to the top. The link is the identity here, the same one the feed
// uses as item id. The result is sorted newest first.
func Merge(items, cached []scrape.Item) []scrape.Item {
	knownSince := make(map[string]time.Time, len(cached))
	for _, item := range cached {
		knownSince[item.Link] = item.AddedAt
	}

	for i, item := range items {
		if addedAt, ok := knownSince[item.Link]; ok {
			items[i].AddedAt = addedAt
		}
	}

	slices.SortStableFunc(items, func(a, b scrape.Item) int {
		return b.AddedAt.Compare(a.AddedAt)
	})

	return items
}
