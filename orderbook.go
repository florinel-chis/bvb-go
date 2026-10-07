package bvb

import (
	"context"
	"errors"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// OrderBook is the main-market (REGS) top-5 order book from an instrument's
// detail page ("Order book piata principala (top 5)"). BVB publishes it delayed
// (Delayed reports the page's 15-minute-delay marker), so it is a market-depth
// snapshot, not a live quote.
type OrderBook struct {
	Ticker    string      `json:"ticker"`
	Market    string      `json:"market"`     // always "REGS" (main market)
	Currency  string      `json:"currency"`   // always "RON"
	UpdatedAt time.Time   `json:"updated_at"` // "Ultima actualizare", Europe/Bucharest; zero if absent
	Delayed   bool        `json:"delayed"`    // page carries BVB's 15-min-delayed data marker (site-wide, so in practice always true)
	Bids      []BookLevel `json:"bids"`       // best (highest) first; at most 5
	Asks      []BookLevel `json:"asks"`       // best (lowest) first; at most 5
}

// BookLevel is one price level of one side of the book.
type BookLevel struct {
	Price  float64 `json:"price"`
	Volume int64   `json:"volume"` // shares
}

// orderBookHeading is the trading tab's section title above the order book.
const orderBookHeading = "Order book piata principala"

// tradingTabLabel is the Romanian label of the detail page's trading tab, the
// server-side tab that renders the order book.
const tradingTabLabel = "Tranzactionare"

var (
	formStateNames = []string{"__VIEWSTATE", "__VIEWSTATEGENERATOR", "__EVENTVALIDATION"}
	tradingTabRe   = regexp.MustCompile(`<input[^>]*type="submit"[^>]*name="(ctl00\$body\$IFTC\$[^"]+)"[^>]*value="` + tradingTabLabel + `"`)
	bookRowRe      = regexp.MustCompile(`(?s)<tr[^>]*>(.*?)</tr>`)
	bookCellRe     = regexp.MustCompile(`(?s)<td[^>]*>(.*?)</td>`)
	bookUpdatedRe  = regexp.MustCompile(`Ultima actualizare:\s*(\d{2}\.\d{2}\.\d{4} \d{2}:\d{2}:\d{2})`)
)

// bucharest is the exchange's time zone. Without a system tz database it falls
// back to EET (+02:00), which is an hour off during summer time.
var bucharest = func() *time.Location {
	if loc, err := time.LoadLocation("Europe/Bucharest"); err == nil {
		return loc
	}
	return time.FixedZone("EET", 2*3600)
}()

// OrderBook fetches the top-5 main-market order book for ticker. The book lives
// on the detail page's server-side "Tranzactionare" tab, so this costs two
// requests: a GET for the page's ASP.NET form state, then the tab's postback.
// An unknown ticker yields ErrUnknownSymbol; an instrument with no resting
// orders yields empty Bids/Asks.
func (c *Client) OrderBook(ctx context.Context, ticker string) (OrderBook, error) {
	pageURL := c.webURL + "/FinancialInstruments/Details/FinancialInstrumentsDetails.aspx?" +
		url.Values{"s": {ticker}}.Encode()
	body, err := c.get(ctx, pageURL)
	if err != nil {
		return OrderBook{}, err
	}
	page := string(body)
	if detailField(page, "Simbol:") == "" && firstMatch(companyNameRe, page) == "" {
		return OrderBook{}, fmt.Errorf("%w: %q", ErrUnknownSymbol, ticker)
	}

	form, err := tradingTabForm(page)
	if err != nil {
		return OrderBook{}, fmt.Errorf("bvb: order book for %q: %w", ticker, err)
	}
	body, err = c.postForm(ctx, pageURL, form)
	if err != nil {
		return OrderBook{}, err
	}
	page = string(body)

	bids, asks, updated, err := parseOrderBook(page)
	if err != nil {
		return OrderBook{}, fmt.Errorf("bvb: order book for %q: %w", ticker, err)
	}
	return OrderBook{
		Ticker:    strings.ToUpper(ticker),
		Market:    "REGS",
		Currency:  "RON",
		UpdatedAt: updated,
		Delayed:   strings.Contains(page, `class="delayedTimeImg"`),
		Bids:      bids,
		Asks:      asks,
	}, nil
}

// tradingTabForm builds the postback that switches the detail page to its
// trading tab: the page's ASP.NET form state plus the tab's submit button. The
// button's name embeds a GUID, so it is read from the page, never hard-coded.
func tradingTabForm(page string) (url.Values, error) {
	form := url.Values{"__EVENTTARGET": {""}, "__EVENTARGUMENT": {""}}
	for _, name := range formStateNames {
		// ASP.NET renders hidden fields as name, id, value in that order; a
		// different order fails loudly (the POST returns the summary tab).
		re := regexp.MustCompile(`id="` + name + `" value="([^"]*)"`)
		if m := re.FindStringSubmatch(page); m != nil {
			form.Set(name, html.UnescapeString(m[1]))
		}
	}
	if form.Get("__VIEWSTATE") == "" {
		return nil, errors.New("page has no __VIEWSTATE (layout changed?)")
	}
	m := tradingTabRe.FindStringSubmatch(page)
	if m == nil {
		return nil, fmt.Errorf("page has no %q tab button (layout changed?)", tradingTabLabel)
	}
	form.Set(m[1], tradingTabLabel)
	return form, nil
}

// parseOrderBook extracts both sides of table#gvMMOrderBook and the
// "Ultima actualizare" time. Each row is: bid depth bar, Bid Vol, Bid, Ask,
// Ask Vol, ask depth bar. The bars are each volume relative to the largest on
// either side, so they are derivable and not parsed. A side's blank cells (a
// thin book) are skipped. The section heading without the table is an empty
// book (an ASP.NET GridView with zero rows renders no <table> by default);
// neither is an error, since then the page is not the trading tab.
func parseOrderBook(page string) (bids, asks []BookLevel, updated time.Time, err error) {
	bids, asks = []BookLevel{}, []BookLevel{}
	start := strings.Index(page, `id="gvMMOrderBook"`)
	table := ""
	switch heading := strings.Index(page, orderBookHeading); {
	case start >= 0:
		table = page[start:]
		if end := strings.Index(table, "</table>"); end >= 0 {
			table = table[:end]
		}
	case heading >= 0:
		start = heading
	default:
		return nil, nil, time.Time{}, errors.New("order book table not found (layout changed?)")
	}

	for _, row := range bookRowRe.FindAllStringSubmatch(table, -1) {
		cells := bookCellRe.FindAllStringSubmatch(row[1], -1)
		if len(cells) < 5 {
			continue // header row (<th>) or malformed
		}
		text := func(i int) string {
			return strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(cells[i][1], "")))
		}
		if l, ok := bookLevel(text(2), text(1)); ok {
			bids = append(bids, l)
		}
		if l, ok := bookLevel(text(3), text(4)); ok {
			asks = append(asks, l)
		}
	}

	if m := bookUpdatedRe.FindStringSubmatch(page[start:]); m != nil {
		if t, perr := time.ParseInLocation("02.01.2006 15:04:05", m[1], bucharest); perr == nil {
			updated = t
		}
	}
	return bids, asks, updated, nil
}

// bookLevel parses one side of a row; ok is false for a blank side.
func bookLevel(price, volume string) (BookLevel, bool) {
	l := BookLevel{Price: parseRoFloat(price), Volume: parseRoInt(volume)}
	return l, l.Price > 0 && l.Volume > 0
}
