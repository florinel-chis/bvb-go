package backtestsource

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/florinel-chis/gobacktest/source"

	bvb "github.com/florinel-chis/bvb-go"
)

// historyServer serves the captured TLV daily-history fixture for every
// /api/history request and records the last query it received.
func historyServer(t *testing.T, lastQuery *url.Values) *httptest.Server {
	t.Helper()
	fixture, err := os.ReadFile(filepath.Join("..", "testdata", "history-tlv-1d.json"))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if lastQuery != nil {
			*lastQuery = r.URL.Query()
		}
		_, _ = w.Write(fixture)
	}))
	t.Cleanup(srv.Close)
	return srv
}

func testClient(srv *httptest.Server) *bvb.Client {
	return bvb.New(bvb.WithDatafeedURL(srv.URL), bvb.WithHTTPClient(srv.Client()))
}

// The fixture holds 30 daily bars from 1781136000 to 1784678400 inclusive.
const (
	firstBarUnix = 1781136000
	lastBarUnix  = 1784678400
)

func TestFetchAllBars(t *testing.T) {
	srv := historyServer(t, nil)
	s := New(testClient(srv))
	start := time.Unix(firstBarUnix, 0)
	end := time.Unix(lastBarUnix+1, 0) // half-open: +1s keeps the last bar
	d, err := s.Fetch(context.Background(), "TLV", start, end, source.D1)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if d.Len() != 30 {
		t.Fatalf("got %d bars, want 30", d.Len())
	}
	times := d.Time()
	if !times[0].Equal(time.Unix(firstBarUnix, 0).UTC()) {
		t.Errorf("first bar = %v, want %v", times[0], time.Unix(firstBarUnix, 0).UTC())
	}
	if got := d.Open()[0]; got != 33.3148 {
		t.Errorf("first open = %v, want 33.3148", got)
	}
}

func TestFetchTrimsToWindow(t *testing.T) {
	srv := historyServer(t, nil)
	s := New(testClient(srv))
	// end at the second bar's timestamp: half-open [start, end) keeps only the
	// first bar (the bar stamped exactly at end is excluded).
	start := time.Unix(firstBarUnix, 0)
	end := time.Unix(1781222400, 0) // second bar timestamp from the fixture
	d, err := s.Fetch(context.Background(), "TLV", start, end, source.D1)
	if err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if d.Len() != 1 {
		t.Fatalf("got %d bars, want 1", d.Len())
	}
}

func TestFetchResolutionMapping(t *testing.T) {
	var q url.Values
	srv := historyServer(t, &q)
	s := New(testClient(srv))
	if _, err := s.Fetch(context.Background(), "TLV",
		time.Unix(firstBarUnix, 0), time.Unix(lastBarUnix+1, 0), source.D1); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if q.Get("rs") != "1D" {
		t.Errorf("rs = %q, want 1D", q.Get("rs"))
	}
	if q.Get("currencyCode") != "RON" {
		t.Errorf("currencyCode = %q, want RON", q.Get("currencyCode"))
	}
	if q.Get("ajust") != "1" {
		t.Errorf("ajust = %q, want 1 (adjusted default)", q.Get("ajust"))
	}
}

func TestFetchCurrencyOption(t *testing.T) {
	var q url.Values
	srv := historyServer(t, &q)
	s := New(testClient(srv), WithCurrency("EUR"), WithAdjusted(false))
	if _, err := s.Fetch(context.Background(), "TLV",
		time.Unix(firstBarUnix, 0), time.Unix(lastBarUnix+1, 0), source.D1); err != nil {
		t.Fatalf("Fetch: %v", err)
	}
	if q.Get("currencyCode") != "EUR" {
		t.Errorf("currencyCode = %q, want EUR", q.Get("currencyCode"))
	}
	if q.Get("ajust") != "0" {
		t.Errorf("ajust = %q, want 0", q.Get("ajust"))
	}
}

func TestFetchUnsupportedInterval(t *testing.T) {
	srv := historyServer(t, nil)
	s := New(testClient(srv))
	for _, iv := range []source.Interval{source.H4, source.M2, source.M10} {
		_, err := s.Fetch(context.Background(), "TLV",
			time.Unix(firstBarUnix, 0), time.Unix(lastBarUnix+1, 0), iv)
		if !errors.Is(err, source.ErrUnsupportedInterval) {
			t.Errorf("%s: err = %v, want ErrUnsupportedInterval", iv, err)
		}
	}
}
