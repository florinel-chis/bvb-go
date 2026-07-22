package bvb

import (
	"context"
	"errors"
	"fmt"
	"html"
	"regexp"
	"strings"
)

// ErrUnknownMarket is wrapped when Instruments is asked for a market that has
// no known list page. Match with errors.Is.
var ErrUnknownMarket = errors.New("bvb: unknown market")

// Market names a BVB market-list page. These five share the same
// server-rendered grid (symbol + ISIN + issuer), so Instruments can scrape
// them uniformly. Indices are not here: they are a different page shape and a
// different notion of "instrument"; index candles are still reachable through
// History/SymbolInfo by their datafeed symbol (e.g. "BET").
type Market string

const (
	Shares       Market = "Shares"
	Bonds        Market = "Bonds"
	FundUnits    Market = "FundUnits"
	Warrants     Market = "Warrants"
	Certificates Market = "Certificates"
)

// marketPath maps a Market to its www.bvb.ro list-page path (verified live).
var marketPath = map[Market]string{
	Shares:       "/FinancialInstruments/Markets/Shares",
	Bonds:        "/FinancialInstruments/Markets/Bonds",
	FundUnits:    "/FinancialInstruments/Markets/FundUnits",
	Warrants:     "/FinancialInstruments/Markets/Warrants",
	Certificates: "/FinancialInstruments/Markets/Certificates",
}

// Instrument is one listed instrument from a market-list page. ISIN may be
// empty for the rare listing whose grid row omits it.
type Instrument struct {
	Ticker string
	ISIN   string
	Name   string // issuer / instrument name
	Market Market
}

// instrumentRowRe matches one grid row's symbol cell and the issuer cell that
// follows it. The row shape is machine-generated and stable:
//
//	<a href="...?s=TLV"><b>TLV</b></a><p ...>ROTLVAACNOR1</p></td><td>BANCA TRANSILVANIA S.A.</td>
//
// Group 1 is the ticker, group 2 the (optional) ISIN, group 3 the name.
var instrumentRowRe = regexp.MustCompile(
	`\?s=([A-Za-z0-9]+)"><b>[^<]*</b></a>(?:<p[^>]*>([^<]*)</p>)?\s*</td>\s*<td[^>]*>([^<]*)</td>`)

// Instruments enumerates the instruments listed on a market's page. It is the
// reliable way to list the universe, since the datafeed search is capped.
func (c *Client) Instruments(ctx context.Context, market Market) ([]Instrument, error) {
	path, ok := marketPath[market]
	if !ok {
		return nil, fmt.Errorf("%w: %q", ErrUnknownMarket, market)
	}
	body, err := c.get(ctx, c.webURL+path)
	if err != nil {
		return nil, err
	}
	return parseInstruments(string(body), market), nil
}

// parseInstruments extracts instruments from a market-list page, de-duplicated
// by ticker in first-seen order.
func parseInstruments(page string, market Market) []Instrument {
	matches := instrumentRowRe.FindAllStringSubmatch(page, -1)
	out := make([]Instrument, 0, len(matches))
	seen := make(map[string]bool, len(matches))
	for _, m := range matches {
		ticker := strings.TrimSpace(m[1])
		if ticker == "" || seen[ticker] {
			continue
		}
		seen[ticker] = true
		out = append(out, Instrument{
			Ticker: ticker,
			ISIN:   strings.TrimSpace(m[2]),
			Name:   strings.TrimSpace(html.UnescapeString(m[3])),
			Market: market,
		})
	}
	return out
}
