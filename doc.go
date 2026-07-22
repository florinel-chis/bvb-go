// Package bvb is a read-only client for the Bucharest Stock Exchange (Bursa de
// Valori București). It talks to two of the exchange's own public backends:
//
//   - The TradingView UDF datafeed at https://wapi.bvb.ro/api, which serves
//     the symbol config, per-symbol metadata, a (server-capped) symbol search,
//     and historical OHLCV candles. These endpoints require no authentication
//     token — a browser-like User-Agent and a bvb.ro Referer are sent
//     defensively, nothing more.
//
//   - The server-rendered market-list pages at https://www.bvb.ro, scraped to
//     enumerate the full instrument universe (ticker + ISIN + name), because
//     the datafeed search is capped and cannot list every symbol.
//
// The client is read-only: BVB exposes no trading API. Redistributing BVB
// price data is the caller's responsibility under BVB's terms of use — this
// package ships code, not data.
//
// Two quirks of the datafeed are handled for you: the daily candle resolution
// code is "1D" (bare "D" is rejected with HTTP 500), exposed here as the D1
// constant; and the /history endpoint requires from, to, ajust, countback and
// currencyCode on every call, all supplied by History.
package bvb
