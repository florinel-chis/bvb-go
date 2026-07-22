package bvb

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ErrUnknownSymbol is wrapped by SymbolInfo when the datafeed does not know a
// ticker. Match with errors.Is.
var ErrUnknownSymbol = errors.New("bvb: unknown symbol")

const (
	defaultDatafeedURL = "https://wapi.bvb.ro"
	defaultWebURL      = "https://www.bvb.ro"

	// userAgent is sent on every request. The endpoints answer generic and
	// even absent user agents, but a browser-like string guards against a
	// future WAF rule at no cost.
	userAgent = "Mozilla/5.0 (compatible; bvb-go)"
	// referer is sent on datafeed requests to mirror what the site's own
	// chart sends; the datafeed does not require it today.
	referer = "https://www.bvb.ro/"

	// maxErrBody caps how much of a non-2xx response body is carried into an
	// APIError.
	maxErrBody = 300
)

// Client is a read-only Bucharest Stock Exchange client. Create it with New;
// the zero value is not usable.
type Client struct {
	datafeedURL string
	webURL      string
	userAgent   string
	client      *http.Client
}

// Option configures a Client.
type Option func(*Client)

// WithHTTPClient replaces the default HTTP client (30s timeout).
func WithHTTPClient(h *http.Client) Option { return func(c *Client) { c.client = h } }

// WithDatafeedURL overrides the wapi.bvb.ro datafeed origin (scheme+host, no
// trailing slash). Intended for tests.
func WithDatafeedURL(u string) Option {
	return func(c *Client) { c.datafeedURL = strings.TrimRight(u, "/") }
}

// WithWebURL overrides the www.bvb.ro origin used for market-list scraping
// (scheme+host, no trailing slash). Intended for tests.
func WithWebURL(u string) Option {
	return func(c *Client) { c.webURL = strings.TrimRight(u, "/") }
}

// WithUserAgent overrides the User-Agent header.
func WithUserAgent(ua string) Option { return func(c *Client) { c.userAgent = ua } }

// New returns a Client pointed at BVB's public backends.
func New(opts ...Option) *Client {
	c := &Client{
		datafeedURL: defaultDatafeedURL,
		webURL:      defaultWebURL,
		userAgent:   userAgent,
		client:      &http.Client{Timeout: 30 * time.Second},
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// APIError is a non-2xx HTTP response from a BVB backend.
type APIError struct {
	Status int    // HTTP status code
	URL    string // requested URL (no secrets are ever sent, so it is safe to surface)
	Body   string // truncated response body
}

func (e *APIError) Error() string {
	if body := strings.TrimSpace(e.Body); body != "" {
		return fmt.Sprintf("bvb: HTTP %d for %s: %s", e.Status, e.URL, body)
	}
	return fmt.Sprintf("bvb: HTTP %d for %s", e.Status, e.URL)
}

// get performs a GET and returns the response body, or an *APIError on a
// non-2xx status.
func (c *Client) get(ctx context.Context, rawURL string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Referer", referer)
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := string(body)
		if len(msg) > maxErrBody {
			msg = msg[:maxErrBody]
		}
		return nil, &APIError{Status: resp.StatusCode, URL: rawURL, Body: msg}
	}
	return body, nil
}

// getJSON performs a GET and JSON-decodes the body into dst.
func (c *Client) getJSON(ctx context.Context, rawURL string, dst any) error {
	body, err := c.get(ctx, rawURL)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, dst); err != nil {
		return fmt.Errorf("bvb: decode %s: %w", rawURL, err)
	}
	return nil
}

// ServerTime returns the datafeed's current time (GET /api/time).
func (c *Client) ServerTime(ctx context.Context) (time.Time, error) {
	body, err := c.get(ctx, c.datafeedURL+"/api/time")
	if err != nil {
		return time.Time{}, err
	}
	sec, err := strconv.ParseInt(strings.TrimSpace(string(body)), 10, 64)
	if err != nil {
		return time.Time{}, fmt.Errorf("bvb: parse server time %q: %w", body, err)
	}
	return time.Unix(sec, 0).UTC(), nil
}

// SymbolType is one instrument class advertised by the datafeed config.
// Value is the single-letter code (S=shares, B=bonds, R=rights, U=fund units,
// T=structured, F=futures, I=indices); it is empty for the "all" pseudo-type.
type SymbolType struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Config is the datafeed configuration (GET /api/config).
type Config struct {
	SupportsSearch       bool         `json:"supports_search"`
	SupportsTime         bool         `json:"supports_time"`
	SymbolTypes          []SymbolType `json:"symbols_types"`
	SupportedResolutions []string     `json:"supported_resolutions"`
}

// Config returns the datafeed configuration.
func (c *Client) Config(ctx context.Context) (Config, error) {
	var cfg Config
	err := c.getJSON(ctx, c.datafeedURL+"/api/config?withNews=false&lang=ro", &cfg)
	return cfg, err
}

// SymbolInfo is per-symbol metadata (GET /api/symbols?symbol=...).
type SymbolInfo struct {
	Name                 string   `json:"name"`
	Description          string   `json:"description"` // issuer/company name
	Exchange             string   `json:"exchange"`
	Type                 string   `json:"type"`
	Ticker               string   `json:"ticker"`
	Session              string   `json:"session"`
	Timezone             string   `json:"timezone"`
	HasIntraday          bool     `json:"has_intraday"`
	SupportedResolutions []string `json:"supported_resolutions"`
}

// SymbolInfo resolves one ticker to its datafeed metadata. An unknown ticker
// is reported as ErrUnknownSymbol: the datafeed answers HTTP 200 with an
// all-empty body rather than an error status, so SymbolInfo treats an empty
// ticker in the response as "not found".
func (c *Client) SymbolInfo(ctx context.Context, ticker string) (SymbolInfo, error) {
	q := url.Values{"symbol": {ticker}}
	var si SymbolInfo
	if err := c.getJSON(ctx, c.datafeedURL+"/api/symbols?"+q.Encode(), &si); err != nil {
		return SymbolInfo{}, err
	}
	if si.Ticker == "" {
		return SymbolInfo{}, fmt.Errorf("%w: %q", ErrUnknownSymbol, ticker)
	}
	return si, nil
}

// SearchResult is one hit from the datafeed symbol search. Description
// typically embeds the ISIN. Type is lowercase (share/bond/structured/...).
type SearchResult struct {
	Symbol      string `json:"symbol"`
	FullName    string `json:"full_name"`
	Description string `json:"description"`
	Exchange    string `json:"exchange"`
	Ticker      string `json:"ticker"`
	Type        string `json:"type"`
}

// searchLimit is sent as the /api/search "limit". The endpoint caps results
// at ~30 regardless, and — critically — the route only binds when query, type,
// exchange AND limit are all present (any missing one yields a 404), so all
// four are always sent even though type/exchange are empty.
const searchLimit = 30

// Search queries the datafeed symbol search. The endpoint caps results
// (about 30) for broad queries, so it is a lookup helper, not a way to
// enumerate the universe — use Instruments for that.
func (c *Client) Search(ctx context.Context, query string) ([]SearchResult, error) {
	q := url.Values{
		"query":    {query},
		"type":     {""},
		"exchange": {""},
		"limit":    {strconv.Itoa(searchLimit)},
	}
	var res []SearchResult
	err := c.getJSON(ctx, c.datafeedURL+"/api/search?"+q.Encode(), &res)
	return res, err
}
