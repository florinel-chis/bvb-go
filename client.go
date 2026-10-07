package bvb

import (
	"bytes"
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

// ErrUnknownSymbol is wrapped by SymbolInfo, Fundamentals and OrderBook when
// BVB does not know a ticker. Match with errors.Is.
var ErrUnknownSymbol = errors.New("bvb: unknown symbol")

const (
	defaultDatafeedURL = "https://wapi.bvb.ro"
	defaultWebURL      = "https://www.bvb.ro"

	// userAgent, accept and acceptLanguage make every request look like the
	// site's own pages loaded in a desktop browser. BVB's backends answer bare
	// clients today, but they throttle and drop connections under load, and a
	// browser-like profile is what the site itself is built for.
	userAgent      = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36"
	accept         = "text/html,application/xhtml+xml,application/xml;q=0.9,application/json;q=0.9,*/*;q=0.8"
	acceptLanguage = "ro-RO,ro;q=0.9,en-US;q=0.8,en;q=0.7"
	// referer mirrors what the site's own chart and pages send.
	referer = "https://www.bvb.ro/"

	// Transient-failure retry defaults: 3 attempts, backoff 1s then 2s. A
	// Retry-After header is honoured up to maxRetryAfter.
	defaultAttempts = 3
	defaultBackoff  = time.Second
	maxRetryAfter   = 10 * time.Second

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
	attempts    int           // total tries per request (>= 1)
	backoff     time.Duration // first retry delay; doubles each retry
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

// WithRetry sets how transient failures are retried: attempts is the total
// number of tries per request (1 disables retrying) and backoff the first
// delay, doubled for each further retry. Transient means HTTP 401 (BVB's burst
// gating), 429, 5xx, or a transport error such as a dropped connection.
func WithRetry(attempts int, backoff time.Duration) Option {
	return func(c *Client) {
		c.attempts = max(1, attempts)
		c.backoff = max(0, backoff)
	}
}

// New returns a Client pointed at BVB's public backends.
func New(opts ...Option) *Client {
	c := &Client{
		datafeedURL: defaultDatafeedURL,
		webURL:      defaultWebURL,
		userAgent:   userAgent,
		client:      &http.Client{Timeout: 30 * time.Second},
		attempts:    defaultAttempts,
		backoff:     defaultBackoff,
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
	return c.do(ctx, http.MethodGet, rawURL, nil, "")
}

// postForm performs a form-encoded POST (an ASP.NET postback) and returns the
// response body, or an *APIError on a non-2xx status.
func (c *Client) postForm(ctx context.Context, rawURL string, form url.Values) ([]byte, error) {
	return c.do(ctx, http.MethodPost, rawURL, []byte(form.Encode()), "application/x-www-form-urlencoded")
}

// do sends a request with the client's browser-like headers, retrying
// transient failures with exponential backoff, and returns the body. Each
// attempt builds a fresh request, so a POST body is re-sent intact. The
// context bounds the whole call, backoff included.
func (c *Client) do(ctx context.Context, method, rawURL string, body []byte, contentType string) ([]byte, error) {
	delay := c.backoff
	for attempt := 1; ; attempt++ {
		out, retryAfter, err := c.once(ctx, method, rawURL, body, contentType)
		if err == nil || attempt >= c.attempts || !transient(err) || ctx.Err() != nil {
			return out, err
		}
		wait := delay
		if retryAfter > 0 {
			wait = min(retryAfter, maxRetryAfter)
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(wait):
		}
		delay *= 2
	}
}

// once performs a single attempt. retryAfter is the server's Retry-After hint
// (0 if absent or unparsable).
func (c *Client) once(ctx context.Context, method, rawURL string, body []byte, contentType string) (out []byte, retryAfter time.Duration, err error) {
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, reader)
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("User-Agent", c.userAgent)
	req.Header.Set("Accept", accept)
	req.Header.Set("Accept-Language", acceptLanguage)
	req.Header.Set("Referer", referer)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, 0, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		msg := string(data)
		if len(msg) > maxErrBody {
			msg = msg[:maxErrBody]
		}
		if secs, perr := strconv.Atoi(strings.TrimSpace(resp.Header.Get("Retry-After"))); perr == nil && secs > 0 {
			retryAfter = time.Duration(secs) * time.Second
		}
		return nil, retryAfter, &APIError{Status: resp.StatusCode, URL: rawURL, Body: msg}
	}
	return data, 0, nil
}

// transient reports whether err is worth retrying: BVB's burst gating (401),
// rate limiting (429), server errors (5xx) and transport failures. Other HTTP
// errors (e.g. 404) and context cancellation are final.
func transient(err error) bool {
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var apiErr *APIError
	if errors.As(err, &apiErr) {
		return apiErr.Status == http.StatusUnauthorized || apiErr.Status == http.StatusTooManyRequests || apiErr.Status >= 500
	}
	return true // transport error: connection reset, EOF, timeout, …
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
