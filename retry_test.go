package bvb

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// retryClient points a client at srv with near-zero backoff so retry tests are fast.
func retryClient(srv *httptest.Server, attempts int) *Client {
	return New(WithDatafeedURL(srv.URL), WithWebURL(srv.URL), WithHTTPClient(srv.Client()),
		WithRetry(attempts, time.Millisecond))
}

func TestBrowserLikeHeaders(t *testing.T) {
	var got http.Header
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Clone()
		_, _ = w.Write([]byte("1784729782"))
	}))
	t.Cleanup(srv.Close)
	if _, err := retryClient(srv, 1).ServerTime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ua := got.Get("User-Agent"); !strings.Contains(ua, "Chrome/") || !strings.HasPrefix(ua, "Mozilla/5.0 (") {
		t.Errorf("User-Agent = %q, want a browser-like Chrome UA", ua)
	}
	for _, h := range []string{"Accept", "Accept-Language", "Referer"} {
		if got.Get(h) == "" {
			t.Errorf("missing %s header", h)
		}
	}
}

func TestWithUserAgentOverrides(t *testing.T) {
	var ua string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ua = r.UserAgent()
		_, _ = w.Write([]byte("1784729782"))
	}))
	t.Cleanup(srv.Close)
	c := New(WithDatafeedURL(srv.URL), WithHTTPClient(srv.Client()), WithUserAgent("custom/1"))
	if _, err := c.ServerTime(context.Background()); err != nil {
		t.Fatal(err)
	}
	if ua != "custom/1" {
		t.Errorf("User-Agent = %q, want custom/1", ua)
	}
}

// flaky answers with fail() for the first n calls, then 200 + body.
func flaky(t *testing.T, n int32, fail func(w http.ResponseWriter), body string) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) <= n {
			fail(w)
			return
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestRetriesBurstGating401(t *testing.T) {
	// BVB answers bursts with 401 "Authorization has been denied for this request."
	srv, calls := flaky(t, 2, func(w http.ResponseWriter) {
		http.Error(w, `{"Message":"Authorization has been denied for this request."}`, http.StatusUnauthorized)
	}, "1784729782")
	if _, err := retryClient(srv, 3).ServerTime(context.Background()); err != nil {
		t.Fatalf("want success after retries, got %v", err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestRetries429And5xx(t *testing.T) {
	for _, status := range []int{http.StatusTooManyRequests, http.StatusBadGateway, http.StatusServiceUnavailable} {
		srv, calls := flaky(t, 1, func(w http.ResponseWriter) {
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(status)
		}, "1784729782")
		if _, err := retryClient(srv, 3).ServerTime(context.Background()); err != nil {
			t.Fatalf("status %d: want success after a retry, got %v", status, err)
		}
		if calls.Load() != 2 {
			t.Errorf("status %d: calls = %d, want 2", status, calls.Load())
		}
	}
}

func TestRetriesTransportError(t *testing.T) {
	// First connection is dropped without a response, like BVB under load.
	srv, calls := flaky(t, 1, func(w http.ResponseWriter) {
		conn, _, err := w.(http.Hijacker).Hijack()
		if err == nil {
			_ = conn.Close()
		}
	}, "1784729782")
	if _, err := retryClient(srv, 3).ServerTime(context.Background()); err != nil {
		t.Fatalf("want success after a dropped connection, got %v", err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
}

func TestGivesUpAfterAttempts(t *testing.T) {
	srv, calls := flaky(t, 100, func(w http.ResponseWriter) { w.WriteHeader(http.StatusServiceUnavailable) }, "")
	_, err := retryClient(srv, 3).ServerTime(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusServiceUnavailable {
		t.Fatalf("err = %v, want *APIError 503", err)
	}
	if calls.Load() != 3 {
		t.Errorf("calls = %d, want 3", calls.Load())
	}
}

func TestNoRetryOnClientErrors(t *testing.T) {
	srv, calls := flaky(t, 100, func(w http.ResponseWriter) { http.NotFound(w, nil) }, "")
	if _, err := retryClient(srv, 3).ServerTime(context.Background()); err == nil {
		t.Fatal("want an error for 404")
	}
	if calls.Load() != 1 {
		t.Errorf("calls = %d, want 1 (a 404 is not transient)", calls.Load())
	}
}

func TestRetryResendsPostBody(t *testing.T) {
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if string(b) != "a=1&b=x%26y" {
			t.Errorf("attempt %d body = %q", calls.Load()+1, b)
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}))
	t.Cleanup(srv.Close)
	body, err := retryClient(srv, 3).postForm(context.Background(), srv.URL+"/x", url.Values{"a": {"1"}, "b": {"x&y"}})
	if err != nil || string(body) != "ok" {
		t.Fatalf("body=%q err=%v", body, err)
	}
	if calls.Load() != 2 {
		t.Errorf("calls = %d, want 2", calls.Load())
	}
}

func TestRetryStopsOnContextCancel(t *testing.T) {
	srv, _ := flaky(t, 100, func(w http.ResponseWriter) { w.WriteHeader(http.StatusUnauthorized) }, "")
	c := New(WithDatafeedURL(srv.URL), WithHTTPClient(srv.Client()), WithRetry(5, time.Second))
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err := c.ServerTime(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err = %v, want context.DeadlineExceeded", err)
	}
	if d := time.Since(start); d > 500*time.Millisecond {
		t.Errorf("took %v: backoff must stop when the context ends", d)
	}
}
