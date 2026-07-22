package bvb

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"time"
)

// ErrUnsupportedResolution is wrapped when History is asked for a resolution
// outside the supported set. Match with errors.Is.
var ErrUnsupportedResolution = errors.New("bvb: unsupported resolution")

// ErrHistoryTruncated is wrapped when the requested span needs more bars than
// a single request can carry (maxCountback) and the delivered window does not
// reach back to from — so the series would be silently truncated. Match with
// errors.Is and retry with a coarser resolution or a later start. This only
// arises for long intraday spans; daily and coarser never hit the cap.
var ErrHistoryTruncated = errors.New("bvb: history window truncated")

// Resolution is a datafeed candle resolution. The constants carry the exact
// codes the /history endpoint accepts — note that daily is "1D", not the
// TradingView-style "D" advertised by SymbolInfo (bare "D" is rejected with
// HTTP 500).
type Resolution string

const (
	M1  Resolution = "1"
	M5  Resolution = "5"
	M15 Resolution = "15"
	M30 Resolution = "30"
	H1  Resolution = "60"
	D1  Resolution = "1D"
	W1  Resolution = "1W"
	Mo1 Resolution = "1M"
)

// barDuration is the nominal wall-clock span of one bar, used to size the
// countback. It returns 0 for an unsupported resolution.
func (r Resolution) barDuration() time.Duration {
	switch r {
	case M1:
		return time.Minute
	case M5:
		return 5 * time.Minute
	case M15:
		return 15 * time.Minute
	case M30:
		return 30 * time.Minute
	case H1:
		return time.Hour
	case D1:
		return 24 * time.Hour
	case W1:
		return 7 * 24 * time.Hour
	case Mo1:
		return 30 * 24 * time.Hour
	default:
		return 0
	}
}

// Valid reports whether r is a supported resolution.
func (r Resolution) Valid() bool { return r.barDuration() != 0 }

const (
	// countbackBuffer is added to the estimated bar count so the returned
	// window reaches back past start despite weekends, holidays, and (for
	// intraday) non-trading hours.
	countbackBuffer = 5
)

// maxCountback bounds a single request. Daily/weekly/monthly history never
// reaches it (BVB has ~7k daily bars at most), so it only ever binds on a
// long intraday span — in which case History fails with ErrHistoryTruncated
// rather than silently returning just the most recent window. It is a var so
// tests can lower it.
var maxCountback = 20000

// Bar is one OHLCV candle. Time is the bar's open time in UTC.
type Bar struct {
	Time   time.Time
	Open   float64
	High   float64
	Low    float64
	Close  float64
	Volume int64
}

// udfHistory is the raw TradingView UDF history response.
type udfHistory struct {
	T []int64   `json:"t"`
	O []float64 `json:"o"`
	H []float64 `json:"h"`
	L []float64 `json:"l"`
	C []float64 `json:"c"`
	V []float64 `json:"v"`
	S string    `json:"s"`
}

// countbackFor estimates how many bars of resolution r span [from, to],
// clamped to [1, maxCountback] with a buffer. The datafeed's countback
// parameter counts trading bars while this counts calendar spans, so the
// estimate deliberately over-requests; callers trim to the exact range.
func countbackFor(from, to time.Time, r Resolution) int {
	d := r.barDuration()
	span := to.Sub(from)
	if span < 0 {
		span = 0
	}
	n := int(span/d) + 1 + countbackBuffer
	if n < 1 {
		n = 1
	}
	if n > maxCountback {
		n = maxCountback
	}
	return n
}

// History fetches OHLCV candles for ticker between from and to at resolution
// res. When adjusted is true, prices are corrected for splits (ajust=1).
// currency selects the pricing currency (e.g. "RON"); empty means "RON".
//
// The datafeed bounds the window by to and by a countback derived from the
// requested span, so the returned bars end at or before to and reach back to
// at least from; trim to an exact range if needed. A "no_data" response
// yields an empty slice and no error.
func (c *Client) History(ctx context.Context, ticker string, from, to time.Time, res Resolution, adjusted bool, currency string) ([]Bar, error) {
	if !res.Valid() {
		return nil, fmt.Errorf("%w: %q", ErrUnsupportedResolution, res)
	}
	if currency == "" {
		currency = "RON"
	}
	ajust := "0"
	if adjusted {
		ajust = "1"
	}
	countback := countbackFor(from, to, res)
	q := url.Values{
		"symbol":       {ticker},
		"from":         {strconv.FormatInt(from.Unix(), 10)},
		"to":           {strconv.FormatInt(to.Unix(), 10)},
		"rs":           {string(res)},
		"ajust":        {ajust},
		"countback":    {strconv.Itoa(countback)},
		"currencyCode": {currency},
	}
	var h udfHistory
	if err := c.getJSON(ctx, c.datafeedURL+"/api/history?"+q.Encode(), &h); err != nil {
		return nil, err
	}
	switch h.S {
	case "ok":
	case "no_data":
		return nil, nil
	default:
		return nil, fmt.Errorf("bvb: history %q: datafeed status %q", ticker, h.S)
	}
	n := len(h.T)
	if len(h.O) != n || len(h.H) != n || len(h.L) != n || len(h.C) != n {
		return nil, fmt.Errorf("bvb: history %q: ragged UDF arrays", ticker)
	}
	bars := make([]Bar, n)
	for i := range h.T {
		var vol int64
		if i < len(h.V) {
			vol = int64(h.V[i])
		}
		bars[i] = Bar{
			Time:   time.Unix(h.T[i], 0).UTC(),
			Open:   h.O[i],
			High:   h.H[i],
			Low:    h.L[i],
			Close:  h.C[i],
			Volume: vol,
		}
	}
	// If the request was capped and the endpoint filled the whole capped
	// window yet its earliest bar still starts after from, the series was
	// truncated by the cap rather than by data availability (a short delivery
	// means the instrument simply has no older bars). Fail loudly instead of
	// handing back a silently shortened window.
	if countback >= maxCountback && len(bars) >= maxCountback && bars[0].Time.After(from) {
		return nil, fmt.Errorf("%w: %q %s reaches back only to %s for a request from %s; use a coarser resolution or a later start",
			ErrHistoryTruncated, ticker, res, bars[0].Time.Format(time.RFC3339), from.UTC().Format(time.RFC3339))
	}
	return bars, nil
}
