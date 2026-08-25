package scrape

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// shortenDelays makes the retry timing test sized instead of production sized.
func shortenDelays(t *testing.T) {
	t.Helper()

	timeout, transient, buffer := fetchTimeout, transientDelay, retryAfterBuffer
	t.Cleanup(func() {
		fetchTimeout, transientDelay, retryAfterBuffer = timeout, transient, buffer
	})

	fetchTimeout, transientDelay, retryAfterBuffer = 200*time.Millisecond, 10*time.Millisecond, 10*time.Millisecond
}

// TestFetchRetriesTransientFailures covers the failures that a second attempt can
// fix. The page succeeds on the third try, which must not surface as an error.
func TestFetchRetriesTransientFailures(t *testing.T) {
	shortenDelays(t)

	tests := []struct {
		name  string
		fail  func(w http.ResponseWriter, r *http.Request)
		wantN int32
	}{
		{"timeout", func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(fetchTimeout + 200*time.Millisecond)
		}, 3},
		{"server error", func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusBadGateway)
		}, 3},
		{"rate limited", func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		}, 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var calls atomic.Int32

			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if calls.Add(1) < tt.wantN {
					tt.fail(w, r)
					return
				}
				w.Write([]byte(`<html><body><div>ok</div></body></html>`))
			}))
			defer srv.Close()

			doc, err := fetchDocument(t.Context(), srv.URL)
			if err != nil {
				t.Fatalf("gave up instead of retrying: %v", err)
			}

			if got := calls.Load(); got != tt.wantN {
				t.Errorf("server saw %d requests, want %d", got, tt.wantN)
			}

			if doc.Find("div").Text() != "ok" {
				t.Error("did not return the successful response")
			}
		})
	}
}

// TestFetchDoesNotRetryPermanentFailures guards the other direction: repeating a
// 404 only wastes time.
func TestFetchDoesNotRetryPermanentFailures(t *testing.T) {
	var calls atomic.Int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	if _, err := fetchDocument(t.Context(), srv.URL); err == nil {
		t.Fatal("expected an error")
	}

	if got := calls.Load(); got != 1 {
		t.Errorf("server saw %d requests, want a single one", got)
	}
}

// TestFetchGivesUp checks the error a caller sees once the attempts are used up.
func TestFetchGivesUp(t *testing.T) {
	shortenDelays(t)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "0")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := fetchDocument(t.Context(), srv.URL)
	if err == nil {
		t.Fatal("expected an error")
	}

	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("unhelpful error: %v", err)
	}
}

func TestRetryAfter(t *testing.T) {
	tests := []struct {
		header string
		want   time.Duration
	}{
		{"60", 60*time.Second + retryAfterBuffer}, // a buffer, sites are not exact
		{"0", retryAfterBuffer},
		{"9999", maxRetryDelay},                       // capped
		{"Wed, 21 Oct 2026 07:28:00 GMT", retryDelay}, // dates fall back
		{"", retryDelay},
	}

	for _, tt := range tests {
		resp := &http.Response{Header: http.Header{}}
		if tt.header != "" {
			resp.Header.Set("Retry-After", tt.header)
		}

		if got := retryAfter(resp); got != tt.want {
			t.Errorf("retryAfter(%q) = %s, want %s", tt.header, got, tt.want)
		}
	}
}

// TestFetchStopsOnShutdown makes sure the retries do not hold up a shutdown.
func TestFetchStopsOnShutdown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "60")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(t.Context())
	time.AfterFunc(100*time.Millisecond, cancel)

	start := time.Now()
	if _, err := fetchDocument(ctx, srv.URL); err == nil {
		t.Fatal("expected an error")
	}

	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("blocked for %s after cancellation", elapsed)
	}
}
