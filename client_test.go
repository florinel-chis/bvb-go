package bvb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// serveFile writes a testdata file as the HTTP response.
func serveFile(t *testing.T, w http.ResponseWriter, path string) {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	if _, err := w.Write(b); err != nil {
		t.Fatalf("write fixture %s: %v", path, err)
	}
}

// newTestClient returns a Client whose datafeed and web origins both point at
// an httptest server serving the captured fixtures.
func newTestClient(t *testing.T) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/time":
			_, _ = w.Write([]byte("1784729782\n"))
		case r.URL.Path == "/api/config":
			serveFile(t, w, "testdata/config.json")
		case r.URL.Path == "/api/symbols":
			serveFile(t, w, "testdata/symbols-tlv.json")
		case r.URL.Path == "/api/search":
			serveFile(t, w, "testdata/search-tlv.json")
		case r.URL.Path == "/api/history":
			serveFile(t, w, "testdata/history-tlv-1d.json")
		case strings.HasPrefix(r.URL.Path, "/FinancialInstruments/Markets/"):
			serveFile(t, w, "testdata/shares.html")
		case strings.HasPrefix(r.URL.Path, "/FinancialInstruments/Details/"):
			serveFile(t, w, "testdata/detail-atb.html")
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return New(WithDatafeedURL(srv.URL), WithWebURL(srv.URL), WithHTTPClient(srv.Client()))
}

func TestServerTime(t *testing.T) {
	got, err := newTestClient(t).ServerTime(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if want := time.Unix(1784729782, 0).UTC(); !got.Equal(want) {
		t.Errorf("ServerTime = %v, want %v", got, want)
	}
}

func TestConfig(t *testing.T) {
	cfg, err := newTestClient(t).Config(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.SymbolTypes) != 8 {
		t.Errorf("SymbolTypes = %d, want 8", len(cfg.SymbolTypes))
	}
	var haveShares bool
	for _, st := range cfg.SymbolTypes {
		if st.Value == "S" && st.Name == "Actiuni" {
			haveShares = true
		}
	}
	if !haveShares {
		t.Errorf("expected an S=Actiuni symbol type, got %+v", cfg.SymbolTypes)
	}
}

func TestSymbolInfo(t *testing.T) {
	si, err := newTestClient(t).SymbolInfo(context.Background(), "TLV")
	if err != nil {
		t.Fatal(err)
	}
	if si.Ticker != "TLV" {
		t.Errorf("Ticker = %q, want TLV", si.Ticker)
	}
	if si.Description != "BANCA TRANSILVANIA S.A." {
		t.Errorf("Description = %q", si.Description)
	}
	if !si.HasIntraday {
		t.Error("HasIntraday = false, want true")
	}
	if si.Timezone != "Europe/Bucharest" {
		t.Errorf("Timezone = %q", si.Timezone)
	}
}

func TestSymbolInfoUnknown(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		serveFile(t, w, "testdata/symbols-unknown.json")
	}))
	defer srv.Close()
	c := New(WithDatafeedURL(srv.URL), WithHTTPClient(srv.Client()))
	_, err := c.SymbolInfo(context.Background(), "NOPE123")
	if !errors.Is(err, ErrUnknownSymbol) {
		t.Errorf("err = %v, want ErrUnknownSymbol", err)
	}
}

func TestCountbackFor(t *testing.T) {
	base := time.Unix(1700000000, 0)
	day := 24 * time.Hour
	cases := []struct {
		name string
		span time.Duration
		res  Resolution
		want int
	}{
		{"daily zero span", 0, D1, 1 + countbackBuffer},
		{"daily 1 day", day, D1, 1 + 1 + countbackBuffer},
		{"daily 10 days", 10 * day, D1, 10 + 1 + countbackBuffer},
		{"weekly 7 days", 7 * day, W1, 1 + 1 + countbackBuffer},
		{"monthly 30 days", 30 * day, Mo1, 1 + 1 + countbackBuffer},
		{"1-minute 30 days clamps to cap", 30 * day, M1, maxCountback},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := countbackFor(base, base.Add(tc.span), tc.res)
			if got != tc.want {
				t.Errorf("countbackFor(%s, span=%s) = %d, want %d", tc.res, tc.span, got, tc.want)
			}
		})
	}
}

func TestSearch(t *testing.T) {
	res, err := newTestClient(t).Search(context.Background(), "TLV")
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Fatal("Search returned no results")
	}
	var haveShare bool
	for _, r := range res {
		if r.Ticker == "TLV" && r.Type == "share" {
			haveShare = true
		}
	}
	if !haveShare {
		t.Errorf("expected the TLV share in results, got %d hits", len(res))
	}
}

// TestSearchRequestParams guards the route-binding contract: /api/search 404s
// unless query, type, exchange AND limit are all present.
func TestSearchRequestParams(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		serveFile(t, w, "testdata/search-tlv.json")
	}))
	defer srv.Close()
	c := New(WithDatafeedURL(srv.URL), WithHTTPClient(srv.Client()))
	if _, err := c.Search(context.Background(), "TLV"); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"query", "type", "exchange", "limit"} {
		if _, ok := got[k]; !ok {
			t.Errorf("search query missing required param %q (route 404s without it)", k)
		}
	}
	if got.Get("query") != "TLV" {
		t.Errorf("query = %q, want TLV", got.Get("query"))
	}
}

func TestHistory(t *testing.T) {
	bars, err := newTestClient(t).History(
		context.Background(), "TLV",
		time.Unix(1781000000, 0), time.Unix(1784729782, 0),
		D1, true, "RON")
	if err != nil {
		t.Fatal(err)
	}
	if len(bars) != 30 {
		t.Fatalf("bars = %d, want 30", len(bars))
	}
	first := bars[0]
	if want := time.Unix(1781136000, 0).UTC(); !first.Time.Equal(want) {
		t.Errorf("first bar time = %v, want %v", first.Time, want)
	}
	if first.Open != 33.3148 || first.High != 33.7168 || first.Low != 33.1225 || first.Close != 33.3847 {
		t.Errorf("first bar OHLC = %v", first)
	}
	if first.Volume != 872453 {
		t.Errorf("first bar volume = %d, want 872453", first.Volume)
	}
}

// TestHistoryRequestParams pins the exact query the client sends, guarding the
// rs=1D (not "D") quirk and the required-params contract.
func TestHistoryRequestParams(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		serveFile(t, w, "testdata/history-tlv-1d.json")
	}))
	defer srv.Close()
	c := New(WithDatafeedURL(srv.URL), WithHTTPClient(srv.Client()))
	if _, err := c.History(context.Background(), "TLV",
		time.Unix(1781000000, 0), time.Unix(1784729782, 0), D1, true, "RON"); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"symbol":       "TLV",
		"rs":           "1D",
		"ajust":        "1",
		"currencyCode": "RON",
	}
	for k, v := range want {
		if got.Get(k) != v {
			t.Errorf("query[%s] = %q, want %q", k, got.Get(k), v)
		}
	}
	for _, k := range []string{"from", "to", "countback"} {
		if got.Get(k) == "" {
			t.Errorf("query missing required param %q", k)
		}
	}
}

func TestHistoryUnadjusted(t *testing.T) {
	var got url.Values
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.URL.Query()
		serveFile(t, w, "testdata/history-tlv-1d.json")
	}))
	defer srv.Close()
	c := New(WithDatafeedURL(srv.URL), WithHTTPClient(srv.Client()))
	if _, err := c.History(context.Background(), "TLV",
		time.Unix(1781000000, 0), time.Unix(1784729782, 0), D1, false, ""); err != nil {
		t.Fatal(err)
	}
	if got.Get("ajust") != "0" {
		t.Errorf("ajust = %q, want 0", got.Get("ajust"))
	}
	if got.Get("currencyCode") != "RON" {
		t.Errorf("currencyCode = %q, want RON (default)", got.Get("currencyCode"))
	}
}

func TestHistoryUnsupportedResolution(t *testing.T) {
	var hit bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hit = true
	}))
	defer srv.Close()
	c := New(WithDatafeedURL(srv.URL), WithHTTPClient(srv.Client()))
	_, err := c.History(context.Background(), "TLV",
		time.Unix(1781000000, 0), time.Unix(1784729782, 0), Resolution("D"), true, "RON")
	if !errors.Is(err, ErrUnsupportedResolution) {
		t.Errorf("err = %v, want ErrUnsupportedResolution", err)
	}
	if hit {
		t.Error("client made an HTTP call for an unsupported resolution")
	}
}

func TestHistoryNoData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"s":"no_data"}`))
	}))
	defer srv.Close()
	c := New(WithDatafeedURL(srv.URL), WithHTTPClient(srv.Client()))
	bars, err := c.History(context.Background(), "TLV",
		time.Unix(1781000000, 0), time.Unix(1784729782, 0), D1, true, "RON")
	if err != nil {
		t.Fatalf("no_data should not be an error: %v", err)
	}
	if len(bars) != 0 {
		t.Errorf("bars = %d, want 0", len(bars))
	}
}

func TestHistoryTruncationDetected(t *testing.T) {
	old := maxCountback
	maxCountback = 3
	defer func() { maxCountback = old }()
	// Three recent 1-minute bars, but the request reaches far further back:
	// the cap (3) is the binding constraint, so the series is truncated.
	body := `{"t":[1784505600,1784505660,1784505720],"o":[1,1,1],"h":[1,1,1],"l":[1,1,1],"c":[1,1,1],"v":[1,1,1],"s":"ok"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := New(WithDatafeedURL(srv.URL), WithHTTPClient(srv.Client()))
	_, err := c.History(context.Background(), "TLV",
		time.Unix(1000000000, 0), time.Unix(1784505721, 0), M1, true, "RON")
	if !errors.Is(err, ErrHistoryTruncated) {
		t.Errorf("err = %v, want ErrHistoryTruncated", err)
	}
}

func TestHistoryShortDeliveryNotTruncated(t *testing.T) {
	old := maxCountback
	maxCountback = 3
	defer func() { maxCountback = old }()
	// Only two bars exist (fewer than the cap): the instrument simply has no
	// older data, so this is a complete series, not a truncation.
	body := `{"t":[1784505600,1784505660],"o":[1,1],"h":[1,1],"l":[1,1],"c":[1,1],"v":[1,1],"s":"ok"}`
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()
	c := New(WithDatafeedURL(srv.URL), WithHTTPClient(srv.Client()))
	bars, err := c.History(context.Background(), "TLV",
		time.Unix(1000000000, 0), time.Unix(1784505721, 0), M1, true, "RON")
	if err != nil {
		t.Fatalf("short delivery must not error: %v", err)
	}
	if len(bars) != 2 {
		t.Errorf("bars = %d, want 2", len(bars))
	}
}

func TestAPIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "boom", http.StatusInternalServerError)
	}))
	defer srv.Close()
	c := New(WithDatafeedURL(srv.URL), WithHTTPClient(srv.Client()))
	_, err := c.Config(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("err = %v, want *APIError", err)
	}
	if apiErr.Status != http.StatusInternalServerError {
		t.Errorf("Status = %d, want 500", apiErr.Status)
	}
}
