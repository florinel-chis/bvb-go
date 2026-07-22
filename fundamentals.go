package bvb

import (
	"context"
	"fmt"
	"html"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Fundamentals is the company snapshot scraped from an instrument's detail
// page: identity, the "Indicatori bursieri" valuation ratios, issue info, and
// the ownership structure. BVB does not publish multi-year financial
// statements as structured data, so this is a current snapshot, not a
// historical track record.
//
// Numeric indicators that the page omits (e.g. PER for a loss-maker, or a
// missing dividend) are left zero.
type Fundamentals struct {
	Ticker   string
	Name     string // issuer name
	ISIN     string
	Type     string // e.g. "Actiuni"
	Segment  string // e.g. "Principal"
	Category string // e.g. "Premium"
	Status   string // e.g. "Tranzactionabila"

	// Indicatori bursieri (current valuation snapshot).
	MarketCap    float64 // Capitalizare
	PER          float64 // price/earnings
	PBV          float64 // price/book
	EPS          float64
	DivYield     float64 // DIVY, in percent
	Dividend     float64 // last dividend per share
	DividendYear int     // the year that dividend belongs to

	// Issue info.
	SharesOutstanding int64     // Numar total actiuni
	NominalValue      float64   // Valoare Nominala
	ShareCapital      float64   // Capital social
	FirstTradeDate    time.Time // Data start tranzactionare

	// Ownership structure (excludes the TOTAL row).
	Shareholders []Shareholder
}

// Shareholder is one row of the ownership table.
type Shareholder struct {
	Name    string
	Shares  int64
	Percent float64
}

// Fundamentals fetches and parses the detail page for ticker. An unknown
// ticker (the page renders without an identity block) yields ErrUnknownSymbol.
func (c *Client) Fundamentals(ctx context.Context, ticker string) (Fundamentals, error) {
	q := url.Values{"s": {ticker}}
	body, err := c.get(ctx, c.webURL+"/FinancialInstruments/Details/FinancialInstrumentsDetails.aspx?"+q.Encode())
	if err != nil {
		return Fundamentals{}, err
	}
	page := string(body)

	f := Fundamentals{
		Ticker:   detailField(page, "Simbol:"),
		Name:     firstMatch(companyNameRe, page),
		ISIN:     detailField(page, "ISIN:"),
		Type:     detailField(page, "Tip:"),
		Segment:  detailField(page, "Segment:"),
		Category: detailField(page, "Categorie:"),
		Status:   detailField(page, "Stare:"),
	}
	if f.Ticker == "" && f.Name == "" {
		return Fundamentals{}, fmt.Errorf("%w: %q", ErrUnknownSymbol, ticker)
	}
	if f.Ticker == "" {
		f.Ticker = strings.ToUpper(ticker)
	}

	f.MarketCap = parseRoFloat(detailField(page, "Capitalizare"))
	f.PER = parseRoFloat(detailField(page, "PER"))
	f.PBV = parseRoFloat(detailField(page, "P/BV"))
	f.EPS = parseRoFloat(detailField(page, "EPS"))
	f.DivYield = parseRoFloat(detailField(page, "DIVY"))
	if m := dividendRe.FindStringSubmatch(page); m != nil {
		f.DividendYear, _ = strconv.Atoi(m[1])
		f.Dividend = parseRoFloat(m[2])
	}

	f.SharesOutstanding = parseRoInt(detailField(page, "Numar total actiuni"))
	f.NominalValue = parseRoFloat(detailField(page, "Valoare Nominala"))
	f.ShareCapital = parseRoFloat(detailField(page, "Capital social"))
	if d := detailField(page, "Data start tranzactionare"); d != "" {
		if t, err := time.Parse("02.01.2006", d); err == nil {
			f.FirstTradeDate = t
		}
	}

	f.Shareholders = parseShareholders(page)
	return f, nil
}

var (
	tagRe         = regexp.MustCompile(`<[^>]*>`)
	companyNameRe = regexp.MustCompile(`(?s)<h2 class="mBot0 large textStyled">\s*([^<]+?)\s*</h2>`)
	dividendRe    = regexp.MustCompile(`(?s)>Dividend\s*\((\d{4})\)\s*</td>\s*<td[^>]*>(.*?)</td>`)
	// shareholderRowRe matches a data row (name, shares, percent). The TOTAL
	// row uses plain <td>s without class="text-right", so it does not match.
	shareholderRowRe = regexp.MustCompile(`(?s)<td>([^<]+)</td>\s*<td class="text-right">([\d.]+)</td>\s*<td class="text-right[^"]*">([\d.,]+)\s*%</td>`)
)

// detailField returns the trimmed, tag-stripped value cell that follows the
// label cell whose text is label (matched exactly, allowing a trailing space
// before </td>).
func detailField(page, label string) string {
	re := regexp.MustCompile(`(?s)>` + regexp.QuoteMeta(label) + `\s*</td>\s*<td[^>]*>(.*?)</td>`)
	m := re.FindStringSubmatch(page)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(tagRe.ReplaceAllString(m[1], "")))
}

func firstMatch(re *regexp.Regexp, s string) string {
	m := re.FindStringSubmatch(s)
	if m == nil {
		return ""
	}
	return strings.TrimSpace(html.UnescapeString(m[1]))
}

func parseShareholders(page string) []Shareholder {
	matches := shareholderRowRe.FindAllStringSubmatch(page, -1)
	out := make([]Shareholder, 0, len(matches))
	for _, m := range matches {
		name := strings.TrimSpace(html.UnescapeString(m[1]))
		if name == "" || strings.EqualFold(name, "TOTAL") {
			continue
		}
		out = append(out, Shareholder{
			Name:    name,
			Shares:  parseRoInt(m[2]),
			Percent: parseRoFloat(m[3]),
		})
	}
	return out
}

// parseRoFloat parses a Romanian-formatted number: "." groups thousands, ","
// is the decimal separator, and a trailing " %" is dropped. An empty value or
// "-" parses to 0.
func parseRoFloat(s string) float64 {
	s = strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(s), "%"))
	if s == "" || s == "-" {
		return 0
	}
	s = strings.ReplaceAll(s, ".", "")
	s = strings.ReplaceAll(s, ",", ".")
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return f
}

// parseRoInt parses a Romanian-formatted integer ("." groups thousands).
func parseRoInt(s string) int64 {
	s = strings.ReplaceAll(strings.TrimSpace(s), ".", "")
	if s == "" {
		return 0
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		return 0
	}
	return n
}
