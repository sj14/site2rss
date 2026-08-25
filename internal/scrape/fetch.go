package scrape

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/PuerkitoBio/goquery"
)

const (
	// fetchAttempts, retryDelay and maxRetryDelay control the backoff once a page
	// is rate limited. retryDelay only applies when the response carries no usable
	// Retry-After header. arte hands out a minute per 429, so the attempts need to
	// cover several of those to keep a busy window from failing the update.
	fetchAttempts = 5
	retryDelay    = 30 * time.Second
	maxRetryDelay = 2 * time.Minute
	fetchTimeout  = 10 * time.Second
)

// rateLimitError marks a response the site asked us to repeat later.
type rateLimitError struct {
	url        string
	retryAfter time.Duration
}

func (e *rateLimitError) Error() string {
	return fmt.Sprintf("rate limited for %q, retry after %s", e.url, e.retryAfter)
}

// retryAfter reads how long the site wants us to wait. The header may also hold
// an HTTP date, which falls back to the fixed delay.
func retryAfter(resp *http.Response) time.Duration {
	seconds, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil {
		return retryDelay
	}

	return min(time.Duration(seconds)*time.Second, maxRetryDelay)
}

// fetchDocument loads and parses a single page. Arte answers with 429 when it
// gets several requests in a short time, so a rate limited page is retried after
// the delay the response asks for instead of losing its items.
func fetchDocument(ctx context.Context, pageURL string) (*goquery.Document, error) {
	client := http.Client{Timeout: fetchTimeout}

	var err error

	for attempt := 1; attempt <= fetchAttempts; attempt++ {
		var doc *goquery.Document

		doc, err = fetchOnce(ctx, &client, pageURL)
		if err == nil {
			return doc, nil
		}

		var limited *rateLimitError
		if !errors.As(err, &limited) {
			return nil, err
		}

		if attempt < fetchAttempts {
			slog.Warn("rate limited, retrying", "url", pageURL, "delay", limited.retryAfter)

			if err := sleep(ctx, limited.retryAfter); err != nil {
				return nil, err
			}
		}
	}

	return nil, err
}

// sleep waits for the given duration, but gives up as soon as the context is
// cancelled. The waits here reach minutes, which is too long to block a shutdown.
func sleep(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func fetchOnce(ctx context.Context, client *http.Client, pageURL string) (*goquery.Document, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL, nil)
	if err != nil {
		return nil, fmt.Errorf("build request for %q: %w", pageURL, err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed loading site (%q): %w", pageURL, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return nil, &rateLimitError{url: pageURL, retryAfter: retryAfter(resp)}
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("non 200 status (%d) for %q", resp.StatusCode, pageURL)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse document (%q): %w", pageURL, err)
	}

	return doc, nil
}
