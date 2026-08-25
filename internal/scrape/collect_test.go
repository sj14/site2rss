package scrape

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/sj14/site2rss/internal/config"
)

// TestCollectGivesUpWhenCancelled makes the site answer with a one minute rate
// limit. Collect must abandon the wait when the context is cancelled, otherwise
// a shutdown would block for the whole backoff.
func TestCollectGivesUpWhenCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	site := config.Site{
		Name: "Test", URL: srv.URL,
		Selector: config.Selector{Item: "div", Link: "href@a", Title: "h3"},
	}

	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	_, err := Collect(ctx, site)
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error")
	}

	if elapsed > 5*time.Second {
		t.Errorf("Collect blocked for %s after the context was cancelled", elapsed)
	}

	t.Logf("gave up after %s with: %v", elapsed.Round(time.Millisecond), err)
}
