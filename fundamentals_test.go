package bvb

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFundamentals(t *testing.T) {
	f, err := newTestClient(t).Fundamentals(context.Background(), "ATB")
	if err != nil {
		t.Fatal(err)
	}

	// Identity.
	if f.Ticker != "ATB" {
		t.Errorf("Ticker = %q, want ATB", f.Ticker)
	}
	if f.Name != "ANTIBIOTICE S.A." {
		t.Errorf("Name = %q", f.Name)
	}
	if f.ISIN != "ROATBIACNOR9" {
		t.Errorf("ISIN = %q", f.ISIN)
	}
	if f.Type != "Actiuni" || f.Segment != "Principal" || f.Category != "Premium" {
		t.Errorf("classification = %q/%q/%q", f.Type, f.Segment, f.Category)
	}
	if f.Status != "Tranzactionabila" {
		t.Errorf("Status = %q", f.Status)
	}

	// Indicatori bursieri.
	if f.MarketCap != 1463516927.20 {
		t.Errorf("MarketCap = %v, want 1463516927.20", f.MarketCap)
	}
	if f.PER != 28.27 || f.PBV != 1.57 || f.EPS != 0.08 || f.DivYield != 0.94 {
		t.Errorf("ratios PER/PBV/EPS/DIVY = %v/%v/%v/%v", f.PER, f.PBV, f.EPS, f.DivYield)
	}
	if f.Dividend != 0.020557 || f.DividendYear != 2024 {
		t.Errorf("dividend = %v (%d), want 0.020557 (2024)", f.Dividend, f.DividendYear)
	}

	// Issue info.
	if f.SharesOutstanding != 671338040 {
		t.Errorf("SharesOutstanding = %d, want 671338040", f.SharesOutstanding)
	}
	if f.NominalValue != 0.10 {
		t.Errorf("NominalValue = %v, want 0.10", f.NominalValue)
	}
	if f.ShareCapital != 67133804.00 {
		t.Errorf("ShareCapital = %v, want 67133804", f.ShareCapital)
	}
	if want := time.Date(1997, 4, 16, 0, 0, 0, 0, time.UTC); !f.FirstTradeDate.Equal(want) {
		t.Errorf("FirstTradeDate = %v, want %v", f.FirstTradeDate, want)
	}

	// Ownership: the TOTAL row is excluded; the top holder is the Ministry.
	if len(f.Shareholders) != 4 {
		t.Fatalf("Shareholders = %d, want 4 (TOTAL excluded)", len(f.Shareholders))
	}
	top := f.Shareholders[0]
	if top.Shares != 355925135 || top.Percent != 53.0172 {
		t.Errorf("top holder = %d shares / %v%%", top.Shares, top.Percent)
	}
	if !strings.Contains(top.Name, "MINISTERUL SANATATII") {
		t.Errorf("top holder name = %q", top.Name)
	}
	var sum float64
	for _, s := range f.Shareholders {
		sum += s.Percent
	}
	if sum < 99 || sum > 101 {
		t.Errorf("shareholder percents sum = %v, want ~100", sum)
	}
}

func TestFundamentalsUnknown(t *testing.T) {
	// A page with no identity block ⇒ ErrUnknownSymbol.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<html><body>not found</body></html>"))
	}))
	defer srv.Close()
	c := New(WithWebURL(srv.URL), WithHTTPClient(srv.Client()))
	_, err := c.Fundamentals(context.Background(), "NOPE")
	if err == nil {
		t.Fatal("expected ErrUnknownSymbol, got nil")
	}
}
