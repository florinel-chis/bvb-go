# bvb-go

A small, read-only Go client for the **Bucharest Stock Exchange** (Bursa de
Valori București), plus a [`gobacktest`](https://github.com/florinel-chis/gobacktest)
`source.Source` adapter that serves native BVB OHLCV candles.

BVB publishes no official developer API, but its own website is backed by a
TradingView-compatible datafeed at `https://wapi.bvb.ro/api` that answers plain
HTTP requests. `bvb-go` talks to that datafeed for candles, config, metadata and
search, and scrapes the server-rendered market-list pages at `https://www.bvb.ro`
to enumerate the instrument universe.

No authentication is required — the client sends a browser-like `User-Agent` and
a `bvb.ro` `Referer`, nothing more. No browser or headless runtime is needed.

## Install

```
go get github.com/florinel-chis/bvb-go
```

## Usage

```go
c := bvb.New()

// Symbol metadata
si, _ := c.SymbolInfo(ctx, "TLV")            // "BANCA TRANSILVANIA S.A.", BVB, has_intraday

// Daily OHLCV, split-adjusted, priced in RON
bars, _ := c.History(ctx, "TLV",
    time.Now().AddDate(-2, 0, 0), time.Now(),
    bvb.D1, true, "RON")

// The full share universe (ticker + ISIN + issuer)
shares, _ := c.Instruments(ctx, bvb.Shares)  // also Bonds, FundUnits, Warrants, Certificates

// Company details + valuation snapshot (P/E, P/BV, EPS, div yield, ownership, …)
f, _ := c.Fundamentals(ctx, "TLV")
```

### As a gobacktest source

```go
import bvbbs "github.com/florinel-chis/bvb-go/backtestsource"

src := bvbbs.New(c)                          // WithCurrency, WithAdjusted
data, _ := src.Fetch(ctx, "TLV", start, end, source.D1)
```

## Data surface

| What | How |
|------|-----|
| OHLCV candles | `History` → `/api/history` (daily back to ~1997, weekly, monthly, and 1/5/15/30/60-minute intraday) |
| Symbol metadata | `SymbolInfo` → `/api/symbols` |
| Symbol search | `Search` → `/api/search` (server-capped ~30; a lookup, not an enumeration) |
| Datafeed config | `Config` → `/api/config` |
| Instrument universe | `Instruments` → market-list HTML (`Shares`/`Bonds`/`FundUnits`/`Warrants`/`Certificates`) |
| Company fundamentals + details | `Fundamentals` → detail page (identity, Indicatori bursieri valuation ratios, issue info, ownership); current snapshot only — no multi-year statements |

Resolutions map to the constants `M1 M5 M15 M30 H1 D1 W1 Mo1`.

## Quirks handled for you

- **Daily is `1D`, not `D`.** The datafeed advertises TradingView-style `"D"` in
  symbol metadata, but `/history` rejects it with HTTP 500. Use `bvb.D1`.
- **`/history` requires `from`, `to`, `ajust`, `countback` and `currencyCode`**
  on every call; `History` always supplies them and sizes `countback` to cover
  the requested span.
- **Indices** are not part of `Instruments` (different page shape), but index
  candles are reachable through `History`/`SymbolInfo` by their datafeed symbol
  (e.g. `BET`).
- **Transient gating.** Under bursty access the datafeed can briefly answer
  `HTTP 401 "Authorization has been denied for this request."`; it clears on
  its own. `History`/etc. surface this as an `*APIError` (whose message
  includes the response body) — space out large multi-symbol scans.
- **Unknown tickers** come back as HTTP 200 with an empty body; `SymbolInfo`
  reports them as `ErrUnknownSymbol` rather than a blank struct.
- **Deep intraday.** A single request carries a bounded number of bars; if a
  long intraday span can't reach `start`, `History` returns `ErrHistoryTruncated`
  instead of a silently shortened series — retry with a coarser resolution.

## Terms

This client accesses BVB's own public backend and ships **code, not data**.
Redistributing BVB price data is your responsibility under
[BVB's terms of use](https://www.bvb.ro/).

## License

MIT — see [LICENSE](LICENSE).
