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

	cached := []scrape.Item{
		{Title: "Alt", Link: "https://example.com/alt", AddedAt: old},
		{Title: "Umbenannt", Link: "https://example.com/umbenannt", AddedAt: old},
	}

	items := []scrape.Item{
		{Title: "Alt", Link: "https://example.com/alt", AddedAt: newer},
		{Title: "Umbenannt, jetzt anders", Link: "https://example.com/umbenannt", AddedAt: newer},
		{Title: "Neu", Link: "https://example.com/neu", AddedAt: newer},
	}

	got := Merge(items, cached)

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
	want := []scrape.Item{{
		Title:   "Film",
		Link:    "https://example.com/film",
		AddedAt: time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC),
	}}

	if err := Save(dir, "Test", want); err != nil {
		t.Fatal(err)
	}

	got, err := Load(dir, "Test")
	if err != nil {
		t.Fatal(err)
	}

	if len(got) != 1 || got[0].Link != want[0].Link || !got[0].AddedAt.Equal(want[0].AddedAt) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}
