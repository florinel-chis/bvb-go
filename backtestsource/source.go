// Package backtestsource adapts the bvb-go client to gobacktest's
// source.Source interface, serving native Bucharest Stock Exchange OHLCV
// candles from the wapi.bvb.ro datafeed. Unlike the Trading 212 adapter, it
// does not delegate to Yahoo: BVB serves its own candles (RON, split-adjusted,
// deep daily history plus intraday).
package backtestsource

import (
	"context"
	"fmt"
	"time"

	backtest "github.com/florinel-chis/gobacktest"
	"github.com/florinel-chis/gobacktest/source"

	bvb "github.com/florinel-chis/bvb-go"
)

// intervals maps canonical source intervals to BVB datafeed resolutions. The
// datafeed has no 2-minute, 10-minute, or 4-hour bar, so source.M2, source.M10
// and source.H4 are unsupported.
var intervals = map[source.Interval]bvb.Resolution{
	source.M1:  bvb.M1,
	source.M5:  bvb.M5,
	source.M15: bvb.M15,
	source.M30: bvb.M30,
	source.H1:  bvb.H1,
	source.D1:  bvb.D1,
	source.W1:  bvb.W1,
	source.Mo1: bvb.Mo1,
}

// Option configures the source returned by New.
type Option func(*src)

// WithCurrency sets the pricing currency passed to the datafeed (default
// "RON"). An empty value is ignored.
func WithCurrency(currency string) Option {
	return func(s *src) {
		if currency != "" {
			s.currency = currency
		}
	}
}

// WithAdjusted controls whether prices are split-adjusted (ajust=1). The
// default is true.
func WithAdjusted(adjusted bool) Option {
	return func(s *src) { s.adjusted = adjusted }
}

type src struct {
	c        *bvb.Client
	currency string
	adjusted bool
}

// New returns a source.Source backed by the given BVB client. Symbols are the
// exchange tickers (e.g. "TLV", "SNP", "BRD").
func New(c *bvb.Client, opts ...Option) source.Source {
	s := &src{c: c, currency: "RON", adjusted: true}
	for _, o := range opts {
		o(s)
	}
	return s
}

// Fetch returns OHLCV bars for symbol on the family-wide half-open [start, end)
// contract. The datafeed bounds the window by end and reaches back past start,
// so Fetch trims the delivery to the requested range.
func (s *src) Fetch(ctx context.Context, symbol string, start, end time.Time, interval source.Interval) (*backtest.Data, error) {
	res, ok := intervals[interval]
	if !ok {
		return nil, fmt.Errorf("bvb: %w: %q", source.ErrUnsupportedInterval, interval)
	}
	candles, err := s.c.History(ctx, symbol, start, end, res, s.adjusted, s.currency)
	if err != nil {
		return nil, err
	}
	bars := make([]backtest.Bar, 0, len(candles))
	for _, cd := range candles {
		if cd.Time.Before(start) || !cd.Time.Before(end) {
			continue
		}
		bars = append(bars, backtest.Bar{
			Time:   cd.Time,
			Open:   cd.Open,
			High:   cd.High,
			Low:    cd.Low,
			Close:  cd.Close,
			Volume: float64(cd.Volume),
		})
	}
	return backtest.FromBars(bars), nil
}
