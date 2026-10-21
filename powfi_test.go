package main

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/michimani/gotwi"
	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
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
		for _, expected := range []string{"Swap: #Powfi", "Sell Volume: 10.00K ALPH", "xALPH", "Estimated value", "https://powfi.alephium.org/swap/", ">TX link</a>"} {
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

func TestDEXDisabledPreventsWatchers(t *testing.T) {
	for _, tc := range []struct{ name, upper, lower string }{
		{"uppercase", "false", "true"},
		{"lowercase", "true", "false"},
		{"numeric", "0", "true"},
		{"invalid", "disabled", "true"},
		{"empty", "", "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("DEX_ENABLED", tc.upper)
			t.Setenv("dex_enabled", tc.lower)
			t.Setenv("POWFI_POOL_ADDRESSES", "configured-pool")
			if dexEnabled() {
				t.Fatal("DEX should be disabled")
			}
			// A nil channel and configured pool must not start any polling.
			startPowfiWatchers(nil)
		})
	}
}

func TestDEXEnabledDefault(t *testing.T) {
	t.Setenv("DEX_ENABLED", "true")
	t.Setenv("dex_enabled", "true")
	if !dexEnabled() {
		t.Fatal("explicit true should enable DEX")
	}
	os.Unsetenv("DEX_ENABLED")
	os.Unsetenv("dex_enabled")
	if !dexEnabled() {
		t.Fatal("unset should preserve enabled default")
	}
}

func TestDEXMissingPrivateGroupPreventsWatchers(t *testing.T) {
	t.Setenv("DEX_ENABLED", "true")
	t.Setenv("dex_enabled", "true")
	t.Setenv("POWFI_POOL_ADDRESSES", "configured-pool")
	for _, value := range []string{"", "0", "invalid", "99999999999999999999999999"} {
		t.Run(value, func(t *testing.T) {
			t.Setenv("PRIVATE_CHAT_ID", value)
			// A configured pool and nil channel must not start any polling.
			startPowfiWatchers(nil)
		})
	}
}

type tradeAlertTestCaller func(string, *ta.RequestData) (*ta.Response, error)

func (f tradeAlertTestCaller) Call(url string, data *ta.RequestData) (*ta.Response, error) {
	return f(url, data)
}

func TestTradeAlertDestinations(t *testing.T) {
	oldTelegram, oldTwitter, oldChat := telegramBot, twitterBot, parameters.TelegramChatId
	t.Cleanup(func() {
		telegramBot, twitterBot, parameters.TelegramChatId = oldTelegram, oldTwitter, oldChat
	})
	parameters.TelegramChatId = -1009999999999
	var sent []struct {
		ChatID int64  `json:"chat_id"`
		Text   string `json:"text"`
		Mode   string `json:"parse_mode"`
	}
	bot, err := telego.NewBot("123456789:"+strings.Repeat("a", 35), telego.WithAPICaller(tradeAlertTestCaller(func(url string, data *ta.RequestData) (*ta.Response, error) {
		if !strings.HasSuffix(url, "/sendMessage") {
			t.Fatalf("unexpected Telegram method: %s", url)
		}
		var message struct {
			ChatID int64  `json:"chat_id"`
			Text   string `json:"text"`
			Mode   string `json:"parse_mode"`
		}
		if err := json.Unmarshal(data.Buffer.Bytes(), &message); err != nil {
			t.Fatal(err)
		}
		sent = append(sent, message)
		return &ta.Response{Ok: true}, nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	telegramBot = bot
	for _, tc := range []struct {
		name, exchange, privateID string
		wantChat                  int64
	}{
		{"private DEX", "Powfi", " -1001234567890 ", -1001234567890},
		{"DEX missing group", "Powfi", "", 0},
		{"DEX zero group", "Powfi", "0", 0},
		{"DEX invalid group", "Powfi", "invalid", 0},
		{"public CEX", "Mexc", "-1001234567890", -1009999999999},
		{"CEX without private group", "Gate.io", "", -1009999999999},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv("PRIVATE_CHAT_ID", tc.privateID)
			sent = nil
			twitterBot = nil
			if tc.exchange == "Powfi" {
				// DEX delivery must never try to use even a configured Twitter client.
				twitterBot = &gotwi.Client{}
			}
			sendTradeAlert(MessageCex{ExchangeName: tc.exchange})
			if tc.wantChat == 0 {
				if len(sent) != 0 {
					t.Fatal("missing private group must not fall back to the public chat")
				}
				return
			}
			if len(sent) != 1 || sent[0].ChatID != tc.wantChat || sent[0].Mode != telego.ModeHTML || !strings.Contains(sent[0].Text, "#"+tc.exchange) {
				t.Fatalf("unexpected Telegram delivery: %+v", sent)
			}
		})
	}
}
