package main

import (
	"strings"
	"testing"
)

func TestDecodePowfiSwap(t *testing.T) {
	for _, tc := range []struct {
		name, a, u, side string
		index            int
		invalid          bool
	}{
		{"sell", "1000000000000000000", "-47238", "sell", 0, false},
		{"buy", "-2000000000000000000", "1000000", "buy", 0, false},
		{"reversed tokens", "1000000", "-2000000000000000000", "buy", 1, false},
		{"liquidity", "1000000000000000000", "1000000", "", 0, true},
		{"malformed", "bad", "1000000", "", 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := powfiEvent{EventIndex: 3, TxID: "tx", Fields: make([]powfiField, 7)}
			event.Fields[2] = powfiField{"I256", tc.a}
			event.Fields[3] = powfiField{"I256", tc.u}
			msg, err := decodePowfiSwap(event, tc.index, Token{ID: powfiUSDT, Symbol: "USDT", Decimals: 6}, 0)
			if tc.invalid {
				if err == nil {
					t.Fatal("expected invalid swap")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if msg.Side != tc.side || msg.TxID != "tx" {
				t.Fatalf("unexpected message: %+v", msg)
			}
			if tc.name == "sell" && (msg.AmountLeft.Value != 1 || msg.AmountFiat.Value != 0.047238) {
				t.Fatalf("wrong decimals: %+v", msg)
			}
			if tc.side == "buy" && (msg.AmountLeft.Value != 2 || msg.AmountFiat.Value != 1 || msg.Price != 0.5) {
				t.Fatalf("wrong volumes: %+v", msg)
			}
		})
	}
}

func TestNonUSDTPowfiSwap(t *testing.T) {
	token := Token{ID: "xalph", Symbol: "xALPH", Decimals: 18}
	for _, index := range []int{0, 1} {
		event := powfiEvent{EventIndex: 3, TxID: "tx", Fields: make([]powfiField, 7)}
		event.Fields[2+index] = powfiField{"I256", "10000000000000000000000"}
		event.Fields[2+1-index] = powfiField{"I256", "-9500000000000000000000"}
		msg, err := decodePowfiSwap(event, index, token, 0.5)
		if err != nil {
			t.Fatal(err)
		}
		if msg.Side != "sell" || msg.AmountFiat != (Amount{5000, "USD"}) || msg.QuoteAmount != (Amount{9500, "xALPH"}) || msg.Price != 0.95 {
			t.Fatalf("unexpected swap: %+v", msg)
		}
		text := formatCexMessage(msg)
		for _, expected := range []string{"Swap: #Powfi", "Sell ALPH", "xALPH", "Estimated value", "https://powfi.alephium.org/swap/"} {
			if !strings.Contains(text, expected) {
				t.Fatalf("missing %q in %s", expected, text)
			}
		}
		for _, price := range []float64{0, -1} {
			if _, err := decodePowfiSwap(event, index, token, price); err == nil {
				t.Fatal("missing price must fail")
			}
		}
	}
}
