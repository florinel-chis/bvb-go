package bvb

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// orderBookServer serves the detail page on GET and the Tranzactionare tab on a
// POST that carries the page's form state and the tab button; anything else
// gets the Sumar page back (which has no order book), like the real site.
func orderBookServer(t *testing.T, getFixture string) *Client {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasPrefix(r.URL.Path, "/FinancialInstruments/Details/") {
			http.NotFound(w, r)
			return
		}
		if r.URL.Query().Get("s") != "ATB" {
			t.Errorf("ticker query = %q, want ATB", r.URL.Query().Get("s"))
		}
		if r.Method != http.MethodPost {
			serveFile(t, w, getFixture)
			return
		}
		if err := r.ParseForm(); err != nil {
			t.Fatal(err)
		}
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("Content-Type = %q", ct)
		}
		ok := r.PostForm.Get("__VIEWSTATE") != "" &&
			r.PostForm.Get("__EVENTVALIDATION") != "" &&
			r.PostForm.Get("__VIEWSTATEGENERATOR") != "" &&
			// The tab button's GUID changes on every render, so the client
			// must echo the one from the page it was served (detail-atb.html).
			r.PostForm.Get("ctl00$body$IFTC$07eeca91-ce47-42f3-a75b-a703b6dafb45") == "Tranzactionare"
		if !ok {
			serveFile(t, w, "testdata/detail-atb.html")
			return
		}
		serveFile(t, w, "testdata/detail-atb-trading.html")
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

func TestParseOrderBookPartial(t *testing.T) {
	// Thin book: three bids, one ask; blank cells must not become zero levels.
	page := `<table id="gvMMOrderBook"><thead><tr><th>&nbsp;</th><th>Bid Vol</th><th>Bid</th><th>Ask</th><th>Ask Vol</th><th>&nbsp;</th></tr></thead><tbody>
<tr><td><span class="sr-only">100</span></td><td align="right">1.200</td><td align="right">10,5000</td><td align="right">10,9000</td><td align="right">300</td><td>x</td></tr>
<tr><td>x</td><td align="right">50</td><td align="right">10,4000</td><td align="right">&nbsp;</td><td align="right">&nbsp;</td><td>&nbsp;</td></tr>
<tr><td>x</td><td align="right">7</td><td align="right">10,0000</td><td align="right"></td><td align="right"></td><td></td></tr>
</tbody></table>
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

func TestParseOrderBookEmpty(t *testing.T) {
	page := `<table id="gvMMOrderBook"><thead><tr><th>Bid Vol</th></tr></thead><tbody></tbody></table>`
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

func TestParseOrderBookMissingTable(t *testing.T) {
	if _, _, _, err := parseOrderBook(`<html>Sumar tab only</html>`); err == nil {
		t.Fatal("want an error when the order book table is absent")
	}
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

func TestParseOrderBookNoTableUnderHeading(t *testing.T) {
	// An ASP.NET GridView with zero rows renders no <table> by default, so a
	// book with no resting orders may be just the section heading.
	page := `<h2 class="styled">Order book piata principala (top 5)</h2>
<div class="wrapped-table"><div></div></div>
<div class="caption">Ultima actualizare: 07.10.2026 18:00:00</div>`
	bids, asks, updated, err := parseOrderBook(page)
	if err != nil {
		t.Fatalf("want an empty book, got error %v", err)
	}
	if bids == nil || asks == nil || len(bids)+len(asks) != 0 {
		t.Errorf("want empty non-nil sides, got %v / %v", bids, asks)
	}
	if updated.IsZero() {
		t.Error("updated should still come from the caption")
	}
}
