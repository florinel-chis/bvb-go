package bvb

import (
	"context"
	"errors"
	"testing"
)

func TestInstruments(t *testing.T) {
	insts, err := newTestClient(t).Instruments(context.Background(), Shares)
	if err != nil {
		t.Fatal(err)
	}
	// The captured Shares page lists ~88 instruments.
	if len(insts) < 80 {
		t.Fatalf("parsed %d instruments, want >= 80", len(insts))
	}
	byTicker := make(map[string]Instrument, len(insts))
	for _, in := range insts {
		if in.Market != Shares {
			t.Errorf("%s: Market = %q, want Shares", in.Ticker, in.Market)
		}
		byTicker[in.Ticker] = in
	}
	tlv, ok := byTicker["TLV"]
	if !ok {
		t.Fatal("TLV not found in parsed instruments")
	}
	if tlv.ISIN != "ROTLVAACNOR1" {
		t.Errorf("TLV ISIN = %q, want ROTLVAACNOR1", tlv.ISIN)
	}
	if tlv.Name != "BANCA TRANSILVANIA S.A." {
		t.Errorf("TLV Name = %q", tlv.Name)
	}
	// A multi-character ticker must survive (guards the ticker charset).
	if _, ok := byTicker["TRANSI"]; !ok {
		t.Error("expected multi-char ticker TRANSI in parsed instruments")
	}
}

func TestInstrumentsUnknownMarket(t *testing.T) {
	_, err := newTestClient(t).Instruments(context.Background(), Market("Nope"))
	if !errors.Is(err, ErrUnknownMarket) {
		t.Errorf("err = %v, want ErrUnknownMarket", err)
	}
}
