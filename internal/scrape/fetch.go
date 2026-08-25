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
	// failed in a way that a later attempt can plausibly fix. retryDelay only
	// applies when the response carries no usable Retry-After header. arte hands
	// out a minute per 429, so the attempts need to cover several of those to keep
	// a busy window from failing the update.
	fetchAttempts = 5
	retryDelay    = 30 * time.Second
	maxRetryDelay = 2 * time.Minute
)

// These are variables rather than constants so the tests can shrink them, real
// timeouts and backoffs would make the suite take minutes.
var (
	fetchTimeout = 10 * time.Second
	// transientDelay is the pause after a timeout, a refused connection or a
	// server error. Those tend to pass on their own, so waiting long is pointless.
	transientDelay = 5 * time.Second
	// retryAfterBuffer is added to the delay a site asks for. Sites are not exact
	// about their own window, and coming back a second too early wastes an attempt.
	retryAfterBuffer = 1 * time.Second
)

// retryableError marks a failure a later attempt can plausibly fix, together
// with how long to wait first. Anything else, a 404 or a page that does not
// parse, is returned as is: repeating it would only cost time.
type retryableError struct {
	url    string
	reason string
	delay  time.Duration
	err    error
}

func (e *retryableError) Error() string {
	if e.err != nil {
		return fmt.Sprintf("%s (%q): %v", e.reason, e.url, e.err)
	}

	return fmt.Sprintf("%s (%q)", e.reason, e.url)
}

func (e *retryableError) Unwrap() error { return e.err }

// retryAfter reads how long the site wants us to wait. The header may also hold
// an HTTP date, which falls back to the fixed delay.
func retryAfter(resp *http.Response) time.Duration {
	seconds, err := strconv.Atoi(resp.Header.Get("Retry-After"))
	if err != nil {
		return retryDelay
	}

	return min(time.Duration(seconds)*time.Second+retryAfterBuffer, maxRetryDelay)
}

// fetchDocument loads and parses a single page, repeating attempts that failed
// for a reason that may well be gone a moment later.
func fetchDocument(ctx context.Context, pageURL string) (*goquery.Document, error) {
	client := http.Client{Timeout: fetchTimeout}

	var err error

	for attempt := 1; attempt <= fetchAttempts; attempt++ {
		var doc *goquery.Document

		doc, err = fetchOnce(ctx, &client, pageURL)
		if err == nil {
			return doc, nil
		}

		// a cancelled context looks like a timeout here, but means we are shutting
		// down and must not keep trying
		if ctx.Err() != nil {
			return nil, err
		}

		var retryable *retryableError
		if !errors.As(err, &retryable) {
			return nil, err
		}

		if attempt < fetchAttempts {
			slog.Warn("fetch failed, retrying",
				"url", pageURL, "reason", retryable.reason, "delay", retryable.delay,
				"attempt", attempt, "of", fetchAttempts)

			if err := sleep(ctx, retryable.delay); err != nil {
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
		// timeouts, refused connections and dns hiccups usually pass by themselves
		return nil, &retryableError{url: pageURL, reason: "request failed", delay: transientDelay, err: err}
	}
	defer resp.Body.Close()

	switch {
	case resp.StatusCode == http.StatusTooManyRequests:
		return nil, &retryableError{url: pageURL, reason: "rate limited", delay: retryAfter(resp)}

	case resp.StatusCode >= http.StatusInternalServerError:
		return nil, &retryableError{
			url:    pageURL,
			reason: fmt.Sprintf("server error %d", resp.StatusCode),
			delay:  transientDelay,
		}

	case resp.StatusCode != http.StatusOK:
		return nil, fmt.Errorf("non 200 status (%d) for %q", resp.StatusCode, pageURL)
	}

	doc, err := goquery.NewDocumentFromReader(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("parse document (%q): %w", pageURL, err)
	}

	return doc, nil
}
