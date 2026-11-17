package cache

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sj14/site2rss/internal/scrape"
)

func TestMerge(t *testing.T) {
	var (
		old   = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		newer = time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	)

	cached := []Entry{
		{Item: scrape.Item{Title: "Alt", Link: "https://example.com/alt", AddedAt: old}, LastSeen: old},
		{Item: scrape.Item{Title: "Umbenannt", Link: "https://example.com/umbenannt", AddedAt: old}, LastSeen: old},
	}

	items := []scrape.Item{
		{Title: "Alt", Link: "https://example.com/alt", AddedAt: newer},
		{Title: "Umbenannt, jetzt anders", Link: "https://example.com/umbenannt", AddedAt: newer},
		{Title: "Neu", Link: "https://example.com/neu", AddedAt: newer},
	}

	got, entries := Merge(items, cached, newer, 24*time.Hour)

	if len(got) != 3 {
		t.Fatalf("got %d items, want 3", len(got))
	}

	// newest first, so the genuinely new item leads
	if got[0].Link != "https://example.com/neu" {
		t.Errorf("first item is %q, want the new one", got[0].Link)
	}

	// a renamed item keeps its timestamp, the link is the identity
	for _, item := range got[1:] {
		if !item.AddedAt.Equal(old) {
			t.Errorf("%q lost its timestamp: %s", item.Link, item.AddedAt)
		}
	}

	// everything listed now is stored as seen now
	if len(entries) != 3 {
		t.Fatalf("got %d entries, want 3", len(entries))
	}

	for _, entry := range entries {
		if !entry.LastSeen.Equal(newer) {
			t.Errorf("%q last seen %s, want %s", entry.Link, entry.LastSeen, newer)
		}
	}
}

// TestMergeRetention covers sites like Joyn, where an item drops off the page
// for a while and comes back: it must not be announced again.
func TestMergeRetention(t *testing.T) {
	var (
		added = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		now   = time.Date(2026, 1, 10, 0, 0, 0, 0, time.UTC)
	)

	retention := 7 * 24 * time.Hour

	cached := []Entry{
		{Item: scrape.Item{Title: "Kurz weg", Link: "https://example.com/kurz", AddedAt: added}, LastSeen: now.Add(-time.Hour)},
		{Item: scrape.Item{Title: "Lange weg", Link: "https://example.com/lange", AddedAt: added}, LastSeen: now.Add(-retention - time.Hour)},
		// written before LastSeen existed
		{Item: scrape.Item{Title: "Alter Cache", Link: "https://example.com/alter-cache", AddedAt: added}},
	}

	t.Run("gone items stay stored but leave the feed", func(t *testing.T) {
		items, entries := Merge(nil, cached, now, retention)

		if len(items) != 0 {
			t.Errorf("got %d items in the feed, want none", len(items))
		}

		if len(entries) != 1 || entries[0].Link != "https://example.com/kurz" {
			t.Errorf("got entries %+v, want only the briefly gone one", entries)
		}

		if !entries[0].LastSeen.Equal(now.Add(-time.Hour)) {
			t.Errorf("LastSeen moved to %s although the item was not listed", entries[0].LastSeen)
		}
	})

	t.Run("a returning item keeps its timestamp", func(t *testing.T) {
		items := []scrape.Item{{Title: "Kurz weg", Link: "https://example.com/kurz", AddedAt: now}}

		got, entries := Merge(items, cached, now, retention)

		if len(got) != 1 || !got[0].AddedAt.Equal(added) {
			t.Errorf("got %+v, want the original timestamp %s", got, added)
		}

		if len(entries) != 1 || !entries[0].LastSeen.Equal(now) {
			t.Errorf("got entries %+v, want it seen now", entries)
		}
	})
}

func TestLoad(t *testing.T) {
	t.Run("missing file is the first run", func(t *testing.T) {
		items, err := Load(t.TempDir(), "Test")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if items != nil {
			t.Errorf("got %v, want no items", items)
		}
	})

	t.Run("broken file is reported", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "Test.json"), []byte("{no json"), 0o644); err != nil {
			t.Fatal(err)
		}

		if _, err := Load(dir, "Test"); err == nil || !strings.Contains(err.Error(), "decode cache") {
			t.Errorf("got %v, want a decode error", err)
		}
	})
}

func TestSaveLoadRoundtrip(t *testing.T) {
	dir := t.TempDir()
	want := []Entry{{
		Item: scrape.Item{
			// & < > must survive without the encoder escaping them for html
			Title:   "Sisi & ich <Film>",
			Link:    "https://example.com/film",
			AddedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
		},
		LastSeen: time.Date(2026, 2, 1, 0, 0, 0, 0, time.UTC),
	}}

	if err := Save(dir, "Test", want); err != nil {
		t.Fatal(err)
	}

	got, err := Load(dir, "Test")
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].Title != want[0].Title || got[0].Link != want[0].Link ||
		!got[0].AddedAt.Equal(want[0].AddedAt) || !got[0].LastSeen.Equal(want[0].LastSeen) {
		t.Errorf("got %+v, want %+v", got, want)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "Test.json"))
	if err != nil {
		t.Fatal(err)
	}

	if !strings.Contains(string(raw), "Sisi & ich <Film>") {
		t.Errorf("the file escapes what it does not need to:\n%s", raw)
	}

	info, err := os.Stat(filepath.Join(dir, "Test.json"))
	if err != nil {
		t.Fatal(err)
	}

	if info.Mode().Perm() != 0o644 {
		t.Errorf("cache file mode is %v, want -rw-r--r--", info.Mode().Perm())
	}

	// the temporary file used for the atomic replace must not survive
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}

	if len(entries) != 1 {
		t.Errorf("cache dir holds %d files, want only the cache itself", len(entries))
	}
}

// TestSaveCreatesDir covers the first run, when the cache directory does not
// exist yet.
func TestSaveCreatesDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing", "cache")

	if err := Save(dir, "Test", []Entry{{Item: scrape.Item{Title: "Film", Link: "https://example.com/film"}}}); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(dir, "Test.json")); err != nil {
		t.Errorf("cache file not written: %v", err)
	}
}
