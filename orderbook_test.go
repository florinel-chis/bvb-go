package bvb

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

// fixtureTabButton is the Tranzactionare button's name as recorded in
// testdata/detail-atb.html; the test server replaces it on every render.
const fixtureTabButton = "ctl00$body$IFTC$07eeca91-ce47-42f3-a75b-a703b6dafb45"

// randomID returns a fresh hex id, standing in for a regenerated ASP.NET id.
func randomID(t *testing.T) string {
	t.Helper()
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(b)
}

// orderBookServer behaves like BVB's detail page, but REGENERATES every id the
// real site might: each GET renders the Tranzactionare button under a new
// name (new naming-container prefix + new GUID), and each POST answers with
// the order book table under a new id. The client only gets the book if it
// echoes the button name from the page it was just served together with the
// page's hidden state; anything else gets the Sumar page back, like the site.
func orderBookServer(t *testing.T, getFixture string) *Client {
	t.Helper()
	var issued string // button name rendered by the latest GET
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/FinancialInstruments/Details/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("s") != "ATB" {
			t.Errorf("ticker query = %q, want ATB", r.URL.Query().Get("s"))
		}
		if r.Method != http.MethodPost {
			issued = "ctl" + randomID(t)[:2] + "$main$TABS$" + randomID(t)
			page := strings.ReplaceAll(readFixture(t, getFixture), fixtureTabButton, issued)
			_, _ = w.Write([]byte(page))
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", ct)
		}
		_, encrypted := r.PostForm["__VIEWSTATEENCRYPTED"] // empty, but a browser sends it
		ok := r.PostForm.Get("__VIEWSTATE") != "" &&
			r.PostForm.Get("__EVENTVALIDATION") != "" &&
			r.PostForm.Get("__VIEWSTATEGENERATOR") != "" && encrypted &&
			r.PostForm.Get(issued) == "Tranzactionare"
		if !ok {
			t.Errorf("postback form rejected: %v", keys(r.PostForm))
			serveFile(t, w, "testdata/detail-atb.html")
			return
		}
		page := strings.ReplaceAll(readFixture(t, "testdata/detail-atb-trading.html"), "gvMMOrderBook", "gv"+randomID(t))
		_, _ = w.Write([]byte(page))
	}))
	t.Cleanup(srv.Close)
	return New(WithWebURL(srv.URL), WithHTTPClient(srv.Client()))
}

func TestOrderBook(t *testing.T) {
	ob, err := orderBookServer(t, "testdata/detail-atb.html").OrderBook(context.Background(), "ATB")
	if err != nil {
		t.Fatal(err)
	}
	if ob.Ticker != "ATB" || ob.Market != "REGS" || ob.Currency != "RON" {
		t.Errorf("identity = %q/%q/%q", ob.Ticker, ob.Market, ob.Currency)
	}
	if !ob.Delayed {
		t.Error("Delayed = false, want true (page marks data 15-min delayed)")
	}
	want := time.Date(2026, 10, 7, 18, 0, 0, 0, time.FixedZone("EEST", 3*3600))
	if !ob.UpdatedAt.Equal(want) {
		t.Errorf("UpdatedAt = %v, want %v", ob.UpdatedAt, want)
	}

	wantBids := []BookLevel{{1.996, 3845}, {1.97, 19119}, {1.966, 1000}, {1.964, 40000}, {1.962, 1455}}
	wantAsks := []BookLevel{{1.998, 6283}, {2.0, 11727}, {2.01, 46339}, {2.02, 1500}, {2.025, 2200}}
	assertLevels(t, "bids", ob.Bids, wantBids)
	assertLevels(t, "asks", ob.Asks, wantAsks)
}

func TestOrderBookUnknownSymbol(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html><body><form id="aspnetForm"></form></body></html>`))
	}))
	t.Cleanup(srv.Close)
	_, err := New(WithWebURL(srv.URL), WithHTTPClient(srv.Client())).OrderBook(context.Background(), "NOPE")
	if !errors.Is(err, ErrUnknownSymbol) {
		t.Fatalf("err = %v, want ErrUnknownSymbol", err)
	}
}

func TestOrderBookLayoutChanged(t *testing.T) {
	// A known instrument whose page lost the Tranzactionare tab button: fail
	// loudly instead of returning an empty book.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<h2 class="mBot0 large textStyled">ANTIBIOTICE S.A.</h2>
<input type="hidden" name="__VIEWSTATE" id="__VIEWSTATE" value="x" />`))
	}))
	t.Cleanup(srv.Close)
	_, err := New(WithWebURL(srv.URL), WithHTTPClient(srv.Client())).OrderBook(context.Background(), "ATB")
	if err == nil || errors.Is(err, ErrUnknownSymbol) {
		t.Fatalf("err = %v, want a layout error", err)
	}
}

// heading is the order book's section title as BVB renders it.
const heading = `<h2 class="styled">Order book piata principala (top 5)</h2>`

func TestParseOrderBookPartial(t *testing.T) {
	// Thin book: three bids, one ask; blank cells must not become zero levels.
	// The table deliberately has no id: it is found from the section heading.
	page := heading + `<div><table class="table"><thead><tr><th>&nbsp;</th><th>Bid Vol</th><th>Bid</th><th>Ask</th><th>Ask Vol</th><th>&nbsp;</th></tr></thead><tbody>
<tr><td><span class="sr-only">100</span></td><td align="right">1.200</td><td align="right">10,5000</td><td align="right">10,9000</td><td align="right">300</td><td>x</td></tr>
<tr><td>x</td><td align="right">50</td><td align="right">10,4000</td><td align="right">&nbsp;</td><td align="right">&nbsp;</td><td>&nbsp;</td></tr>
<tr><td>x</td><td align="right">7</td><td align="right">10,0000</td><td align="right"></td><td align="right"></td><td></td></tr>
</tbody></table></div>
<div class="caption">Ultima actualizare: 15.01.2026 10:31:07</div>`
	bids, asks, updated, err := parseOrderBook(page)
	if err != nil {
		t.Fatal(err)
	}
	assertLevels(t, "bids", bids, []BookLevel{{10.5, 1200}, {10.4, 50}, {10.0, 7}})
	assertLevels(t, "asks", asks, []BookLevel{{10.9, 300}})
	// January → EET (+02:00): the zone must follow Bucharest DST, not a fixed offset.
	if want := time.Date(2026, 1, 15, 10, 31, 7, 0, time.FixedZone("EET", 2*3600)); !updated.Equal(want) {
		t.Errorf("updated = %v, want %v", updated, want)
	}
}

func TestParseOrderBookColumnsByHeader(t *testing.T) {
	// Columns are mapped by their header text, not position: no depth bars,
	// asks first, mixed-case headers.
	page := heading + `<table><tr><th>ask</th><th>ASK VOL</th><th>Bid</th><th>Bid Vol</th></tr>
<tr><td>2,0100</td><td>46.339</td><td>1,9960</td><td>3.845</td></tr></table>`
	bids, asks, _, err := parseOrderBook(page)
	if err != nil {
		t.Fatal(err)
	}
	assertLevels(t, "bids", bids, []BookLevel{{1.996, 3845}})
	assertLevels(t, "asks", asks, []BookLevel{{2.01, 46339}})
}

func TestParseOrderBookEmpty(t *testing.T) {
	// Header row only (GridView with ShowHeaderWhenEmpty), no caption.
	page := heading + `<table><thead><tr><th>Bid Vol</th><th>Bid</th><th>Ask</th><th>Ask Vol</th></tr></thead><tbody></tbody></table>`
	bids, asks, updated, err := parseOrderBook(page)
	if err != nil {
		t.Fatal(err)
	}
	if bids == nil || asks == nil || len(bids)+len(asks) != 0 {
		t.Errorf("want empty non-nil sides, got %v / %v", bids, asks)
	}
	if !updated.IsZero() {
		t.Errorf("updated = %v, want zero (no caption)", updated)
	}
}

func TestParseOrderBookNoTableUnderHeading(t *testing.T) {
	// An ASP.NET GridView with zero rows renders no <table> by default, so an
	// empty book may be just the heading. The NEXT section's table (today's
	// trades) must not be mistaken for the book, nor its caption used.
	page := heading + `<div class="wrapped-table"><div></div></div>
<div class="caption">Ultima actualizare: 07.10.2026 18:00:00</div>
<h2 class="styled">Ultimele tranzactii de astazi</h2>
<table><tr><th>Bid Vol</th><th>Bid</th><th>Ask</th><th>Ask Vol</th></tr>
<tr><td>9</td><td>9,0000</td><td>9,1000</td><td>9</td></tr></table>
<div class="caption">Ultima actualizare: 01.01.2020 00:00:00</div>`
	bids, asks, updated, err := parseOrderBook(page)
	if err != nil {
		t.Fatalf("want an empty book, got error %v", err)
	}
	if bids == nil || asks == nil || len(bids)+len(asks) != 0 {
		t.Errorf("want empty non-nil sides, got %v / %v", bids, asks)
	}
	if want := time.Date(2026, 10, 7, 18, 0, 0, 0, time.FixedZone("EEST", 3*3600)); !updated.Equal(want) {
		t.Errorf("updated = %v, want %v (the book's own caption)", updated, want)
	}
}

func TestParseOrderBookUnknownColumns(t *testing.T) {
	// Rows under headers that are not the book's columns: layout changed.
	page := heading + `<table><tr><th>Pret</th><th>Volum</th></tr><tr><td>1,0</td><td>5</td><td>x</td><td>y</td></tr></table>`
	if _, _, _, err := parseOrderBook(page); err == nil {
		t.Fatal("want a layout error for unrecognised columns")
	}
}

func TestParseOrderBookMissingSection(t *testing.T) {
	if _, _, _, err := parseOrderBook(`<html>Sumar tab only</html>`); err == nil {
		t.Fatal("want an error when the order book section is absent")
	}
}

func TestTradingTabForm(t *testing.T) {
	// Attribute order and case vary, the button's name is whatever the page
	// says, nameless inputs and other tabs' buttons are not sent, and every
	// named hidden field is — like a browser submitting the clicked tab.
	page := `<form method="post" action="./x.aspx?s=ATB">
<input Value="Grafice" NAME="ctl00$x$other" type="submit" />
<input value='Tranzactionare' class="btn" name="zz$9f$TAB-abc" TYPE="SUBMIT">
<input type="hidden" name="__VIEWSTATE" id="__VIEWSTATE" value="a&amp;b" />
<input name="__VIEWSTATEENCRYPTED" type="hidden" value="" />
<input type="hidden" ID="hAdd2PO" Value="Adauga la PORTOFOLIU" />
<input type="text" id="autocomplete-form" placeholder="Nume" />
</form>`
	form, err := tradingTabForm(page)
	if err != nil {
		t.Fatal(err)
	}
	want := url.Values{
		"zz$9f$TAB-abc":        {"Tranzactionare"},
		"__VIEWSTATE":          {"a&b"},
		"__VIEWSTATEENCRYPTED": {""},
	}
	if got := form.Encode(); got != want.Encode() {
		t.Errorf("form = %s\nwant   %s", got, want.Encode())
	}
}

func TestTradingTabFormNoButton(t *testing.T) {
	if _, err := tradingTabForm(`<form><input type="hidden" name="__VIEWSTATE" value="x"></form>`); err == nil {
		t.Fatal("want an error when the Tranzactionare button is absent")
	}
}

func readFixture(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read fixture %s: %v", path, err)
	}
	return string(b)
}

func keys(v url.Values) []string {
	out := make([]string, 0, len(v))
	for k := range v {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func assertLevels(t *testing.T, side string, got, want []BookLevel) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d levels %v, want %d %v", side, len(got), got, len(want), want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("%s[%d] = %+v, want %+v", side, i, got[i], want[i])
		}
	}
}

func TestParseOrderBookEmptyDataText(t *testing.T) {
	// A GridView with EmptyDataText renders one full-width message cell and no
	// column headers: that is an empty book, not a layout change.
	page := heading + `<table><tr><td colspan="6">Nu exista ordine</td></tr></table>`
	bids, asks, _, err := parseOrderBook(page)
	if err != nil {
		t.Fatalf("want an empty book, got error %v", err)
	}
	if len(bids)+len(asks) != 0 {
		t.Errorf("want empty sides, got %v / %v", bids, asks)
	}
}

func TestCellTextSeparatesTags(t *testing.T) {
	// Tags separate words (as in the Python parser): "Bid<br/>Vol" is "Bid Vol".
	if got := cellText("Bid<br/>Vol&nbsp; "); got != "Bid Vol" {
		t.Errorf("cellText = %q, want %q", got, "Bid Vol")
	}
}
