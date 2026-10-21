package main

import (
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/michimani/gotwi"
	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
)

func signalTestSwap(id, side string, usd float64) MessageCex {
	return MessageCex{ExchangeName: "Powfi", TxID: id, Side: side, AmountFiat: Amount{usd, "USD"}}
}

func TestRepeatedDexSwapsWindowAndDeduplication(t *testing.T) {
	m := dexSignalMonitor{pool: "pool", quote: "USDT", cfg: dexSignalConfig{window: 2 * time.Minute, repeatCount: 3, repeatMinUSD: 5000}}
	start := time.Unix(1000, 0)
	initial := []MessageCex{signalTestSwap("a", "buy", 5000), signalTestSwap("a", "buy", 5000), signalTestSwap("small", "buy", 4999), signalTestSwap("b", "buy", 6000), signalTestSwap("sell", "sell", 7000)}
	if alerts := m.observeBatch(initial, start); len(alerts) != 0 {
		t.Fatalf("duplicates, small swaps and opposite sides must not inflate repeated buys: %+v", alerts)
	}
	alerts := m.observeBatch([]MessageCex{signalTestSwap("c", "buy", 7000)}, start.Add(time.Minute))
	if len(alerts) != 1 || alerts[0].kind != "repeated" || alerts[0].side != "buy" || alerts[0].count != 3 || alerts[0].volumeUSD != 18000 || alerts[0].pool != "pool" {
		t.Fatalf("incorrect repeated buy alert: %+v", alerts)
	}
	if alerts := m.observeBatch([]MessageCex{signalTestSwap("d", "buy", 8000)}, start.Add(90*time.Second)); len(alerts) != 0 {
		t.Fatal("remaining above the threshold must not repeat the alert")
	}
	if alerts := m.observeBatch(nil, start.Add(2*time.Minute)); len(alerts) != 0 || m.repeatAbove[0] {
		t.Fatal("expiry must rearm without emitting a new alert")
	}
	alerts = m.observeBatch([]MessageCex{signalTestSwap("e", "buy", 5000)}, start.Add(2*time.Minute))
	if len(alerts) != 1 || alerts[0].count != 3 || alerts[0].volumeUSD != 20000 {
		t.Fatalf("incorrect alert after expiry: %+v", alerts)
	}
	otherPool := dexSignalMonitor{cfg: m.cfg}
	if alerts := otherPool.observeBatch([]MessageCex{signalTestSwap("e", "buy", 5000)}, start); len(alerts) != 0 {
		t.Fatal("pool activity must stay separate")
	}
}

func TestDexSignalsEvaluateWholeBatchAndBothSides(t *testing.T) {
	m := dexSignalMonitor{cfg: dexSignalConfig{window: time.Minute, repeatCount: 2, repeatMinUSD: 5000, imbalancePercent: 80, imbalanceMinVolumeUSD: 10000, imbalanceMinSwaps: 2}}
	batch := []MessageCex{signalTestSwap("a", "buy", 5000), signalTestSwap("b", "buy", 5000), signalTestSwap("c", "sell", 5000), signalTestSwap("d", "sell", 5000)}
	alerts := m.observeBatch(batch, time.Unix(1000, 0))
	if len(alerts) != 2 || alerts[0].kind != "repeated" || alerts[0].side != "buy" || alerts[1].kind != "repeated" || alerts[1].side != "sell" {
		t.Fatalf("expected repeated buys and sells, with no transient imbalance: %+v", alerts)
	}
}

func TestDexImbalanceVolumeSampleSizeAndDirection(t *testing.T) {
	m := dexSignalMonitor{cfg: dexSignalConfig{window: 5 * time.Minute, imbalancePercent: 80, imbalanceMinVolumeUSD: 25000, imbalanceMinSwaps: 5}}
	start := time.Unix(1000, 0)
	batch := []MessageCex{signalTestSwap("a", "buy", 17000), signalTestSwap("b", "buy", 1000), signalTestSwap("c", "buy", 1000), signalTestSwap("d", "buy", 1000)}
	if alerts := m.observeBatch(batch, start); len(alerts) != 0 {
		t.Fatal("insufficient sample or volume must not alert")
	}
	alerts := m.observeBatch([]MessageCex{signalTestSwap("e", "sell", 5000)}, start.Add(time.Second))
	if len(alerts) != 1 || alerts[0].kind != "imbalance" || alerts[0].side != "buy" || alerts[0].share != 80 || alerts[0].buyUSD != 20000 || alerts[0].sellUSD != 5000 || alerts[0].count != 5 {
		t.Fatalf("incorrect 80 percent volume imbalance: %+v", alerts)
	}
	if alerts := m.observeBatch([]MessageCex{signalTestSwap("f", "buy", 5000)}, start.Add(2*time.Second)); len(alerts) != 0 {
		t.Fatal("persistent buy imbalance must not repeat")
	}
	if alerts := m.observeBatch([]MessageCex{signalTestSwap("g", "sell", 20000)}, start.Add(3*time.Second)); len(alerts) != 0 || m.imbalanceSide != "" {
		t.Fatal("balanced volume must clear the active imbalance")
	}
	alerts = m.observeBatch([]MessageCex{signalTestSwap("h", "sell", 75000)}, start.Add(4*time.Second))
	if len(alerts) != 1 || alerts[0].side != "sell" || alerts[0].share != 80 {
		t.Fatalf("expected opposite-side imbalance: %+v", alerts)
	}
	if alerts := m.observeBatch(nil, start.Add(6*time.Minute)); len(alerts) != 0 || len(m.swaps) != 0 || len(m.seen) != 0 || m.imbalanceSide != "" {
		t.Fatal("expiry must clear history and imbalance without alerting")
	}
}

func TestDexImbalanceUsesVolumeNotSwapCount(t *testing.T) {
	m := dexSignalMonitor{cfg: dexSignalConfig{window: time.Minute, imbalancePercent: 80, imbalanceMinVolumeUSD: 25000, imbalanceMinSwaps: 5}}
	batch := []MessageCex{signalTestSwap("big", "sell", 24000)}
	for i := 0; i < 4; i++ {
		batch = append(batch, signalTestSwap(fmt.Sprint(i), "buy", 250))
	}
	alerts := m.observeBatch(batch, time.Unix(1000, 0))
	if len(alerts) != 1 || alerts[0].side != "sell" || alerts[0].share != 96 {
		t.Fatalf("one large sell should dominate volume despite four small buys: %+v", alerts)
	}
}

func TestDexSignalsRejectInvalidObservations(t *testing.T) {
	m := dexSignalMonitor{cfg: dexSignalConfig{window: time.Minute, repeatCount: 2, repeatMinUSD: 1}}
	batch := []MessageCex{signalTestSwap("", "buy", 100), signalTestSwap("side", "other", 100), signalTestSwap("zero", "buy", 0), signalTestSwap("negative", "buy", -100), signalTestSwap("nan", "buy", math.NaN()), signalTestSwap("inf", "buy", math.Inf(1))}
	cex := signalTestSwap("cex", "buy", 100)
	cex.ExchangeName = "Mexc"
	quote := signalTestSwap("quote", "buy", 100)
	quote.AmountFiat.Symbol = "ALPH"
	batch = append(batch, cex, quote)
	if alerts := m.observeBatch(batch, time.Unix(1000, 0)); len(alerts) != 0 || len(m.swaps) != 0 {
		t.Fatal("invalid trades must not affect signal counts or volumes")
	}
}

func TestPublishPowfiBatchIncludesSmallSwapsForSignals(t *testing.T) {
	oldThreshold := parameters.MinAmountDexTriggerUsd
	t.Cleanup(func() { parameters.MinAmountDexTriggerUsd = oldThreshold })
	parameters.MinAmountDexTriggerUsd = 5000
	m := &dexSignalMonitor{pool: "pool", cfg: dexSignalConfig{window: time.Minute, imbalancePercent: 80, imbalanceMinVolumeUSD: 25000, imbalanceMinSwaps: 5}}
	trades := make(chan MessageCex, 5)
	signals := make(chan dexSignalAlert, 5)
	var decoded []MessageCex
	for i := 0; i < 5; i++ {
		event := powfiEvent{EventIndex: 3, TxID: fmt.Sprint(i), Fields: make([]powfiField, 7)}
		event.Fields[2] = powfiField{"I256", "-1000000000000000000"}
		event.Fields[3] = powfiField{"I256", "5000000000"}
		if i == 4 {
			event.Fields[2].Value = "1000000000000000000"
			event.Fields[3].Value = "-4999000000"
		}
		msg, err := decodePowfiSwap(event, 0, Token{ID: powfiUSDT, Symbol: "USDT", Decimals: 6}, 0)
		if err != nil {
			t.Fatal(err)
		}
		decoded = append(decoded, msg)
	}
	// Lower the signal's volume floor by $1: the $4,999 sell is excluded from
	// individual alerts but must keep this batch just above an 80% buy share.
	m.cfg.imbalanceMinVolumeUSD = 24999
	publishPowfiBatch("pool", decoded, m, trades, signals)
	if len(trades) != 4 || len(signals) != 1 {
		t.Fatalf("expected four individual swaps and one signal, got %d and %d", len(trades), len(signals))
	}
	alert := <-signals
	notificationQueueMetric.Dec()
	cexQueueMetrics.Sub(float64(len(trades)))
	if alert.count != 5 || alert.buyUSD != 20000 || alert.sellUSD != 4999 || alert.share <= 80 {
		t.Fatalf("small swaps were lost from the signal: %+v", alert)
	}
}

func TestDexSignalConfig(t *testing.T) {
	keys := []string{"DEX_SIGNAL_WINDOW", "DEX_REPEATED_SWAP_COUNT", "DEX_REPEATED_SWAP_MIN_USD", "DEX_IMBALANCE_PERCENT", "DEX_IMBALANCE_MIN_VOLUME_USD", "DEX_IMBALANCE_MIN_SWAPS"}
	for _, key := range keys {
		t.Setenv(key, "")
	}
	defaults := loadDexSignalConfig()
	if defaults.repeatCount != 0 || defaults.imbalancePercent != 0 || defaults.window != 5*time.Minute || defaults.repeatMinUSD != 5000 || defaults.imbalanceMinVolumeUSD != 25000 || defaults.imbalanceMinSwaps != 5 {
		t.Fatalf("unexpected defaults: %+v", defaults)
	}
	t.Setenv("DEX_REPEATED_SWAP_COUNT", "3")
	t.Setenv("DEX_IMBALANCE_PERCENT", "80")
	for _, tc := range []struct {
		key, value              string
		repeatOff, imbalanceOff bool
	}{
		{"DEX_REPEATED_SWAP_COUNT", "1", true, false},
		{"DEX_REPEATED_SWAP_COUNT", "bad", true, false},
		{"DEX_REPEATED_SWAP_MIN_USD", "NaN", true, false},
		{"DEX_REPEATED_SWAP_MIN_USD", "-1", true, false},
		{"DEX_REPEATED_SWAP_MIN_USD", "0", true, false},
		{"DEX_IMBALANCE_PERCENT", "50", false, true},
		{"DEX_IMBALANCE_PERCENT", "101", false, true},
		{"DEX_IMBALANCE_PERCENT", "Inf", false, true},
		{"DEX_IMBALANCE_MIN_VOLUME_USD", "NaN", false, true},
		{"DEX_IMBALANCE_MIN_VOLUME_USD", "0", false, true},
		{"DEX_IMBALANCE_MIN_SWAPS", "1", false, true},
		{"DEX_SIGNAL_WINDOW", "0", true, true},
		{"DEX_SIGNAL_WINDOW", "-5m", true, true},
		{"DEX_SIGNAL_WINDOW", "bad", true, true},
	} {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			t.Setenv(tc.key, tc.value)
			cfg := loadDexSignalConfig()
			if (cfg.repeatCount == 0) != tc.repeatOff || (cfg.imbalancePercent == 0) != tc.imbalanceOff {
				t.Fatalf("invalid config should disable only affected signals: %+v", cfg)
			}
		})
	}
	t.Setenv("DEX_SIGNAL_WINDOW", " 10m ")
	t.Setenv("DEX_REPEATED_SWAP_MIN_USD", "1000")
	t.Setenv("DEX_IMBALANCE_PERCENT", "100")
	t.Setenv("DEX_IMBALANCE_MIN_VOLUME_USD", "10000")
	t.Setenv("DEX_IMBALANCE_MIN_SWAPS", "10")
	cfg := loadDexSignalConfig()
	if cfg.window != 10*time.Minute || cfg.repeatCount != 3 || cfg.repeatMinUSD != 1000 || cfg.imbalancePercent != 100 || cfg.imbalanceMinVolumeUSD != 10000 || cfg.imbalanceMinSwaps != 10 {
		t.Fatalf("valid overrides not applied: %+v", cfg)
	}
}

func TestDexSignalPrivateDeliveryAndFormatting(t *testing.T) {
	oldTelegram, oldTwitter := telegramBot, twitterBot
	t.Cleanup(func() { telegramBot, twitterBot = oldTelegram, oldTwitter })
	twitterBot = &gotwi.Client{} // Any attempt to post to Twitter fails this test.
	var messages []struct {
		ChatID    int64  `json:"chat_id"`
		Text      string `json:"text"`
		ParseMode string `json:"parse_mode"`
	}
	bot, err := telego.NewBot("123456789:"+strings.Repeat("a", 35), telego.WithAPICaller(tradeAlertTestCaller(func(_ string, data *ta.RequestData) (*ta.Response, error) {
		var msg struct {
			ChatID    int64  `json:"chat_id"`
			Text      string `json:"text"`
			ParseMode string `json:"parse_mode"`
		}
		if err := json.Unmarshal(data.Buffer.Bytes(), &msg); err != nil {
			t.Fatal(err)
		}
		messages = append(messages, msg)
		return &ta.Response{Ok: true}, nil
	})))
	if err != nil {
		t.Fatal(err)
	}
	telegramBot = bot
	for _, kind := range []string{"repeated", "imbalance"} {
		alert := dexSignalAlert{kind: kind, pool: "pool", quote: "USDT", side: "buy", window: 5 * time.Minute, count: 3, volumeUSD: 18000, minSwapUSD: 5000, buyUSD: 20000, sellUSD: 5000, share: 80}
		for _, value := range []string{"", "0", "invalid"} {
			t.Setenv("PRIVATE_CHAT_ID", value)
			sendDexSignalAlert(alert)
		}
		if len(messages) != 0 {
			t.Fatal("invalid private group must not send a signal elsewhere")
		}
		t.Setenv("PRIVATE_CHAT_ID", "-1001234567890")
		sendDexSignalAlert(alert)
		if len(messages) != 1 || messages[0].ChatID != -1001234567890 || messages[0].ParseMode != telego.ModeHTML || !strings.Contains(messages[0].Text, "5 minutes") || !strings.Contains(messages[0].Text, ">View pool</a>") {
			t.Fatalf("incorrect private delivery: %+v", messages)
		}
		if kind == "repeated" && (!strings.Contains(messages[0].Text, "3 large ALPH buys") || !strings.Contains(messages[0].Text, "18.00K USD")) {
			t.Fatal("missing repeated swap details")
		}
		if kind == "imbalance" && (!strings.Contains(messages[0].Text, "80.0%") || !strings.Contains(messages[0].Text, "Buys: 20.00K USD")) {
			t.Fatal("missing imbalance details")
		}
		alert.quote = "<b>unsafe</b>"
		alert.pool = `pool"&`
		if text := alert.format(); strings.Contains(text, "<b>") || !strings.Contains(text, "pool&#34;&amp;") {
			t.Fatal("signal metadata must be HTML escaped")
		}
		alert.quote = strings.Repeat("🚨", 4096)
		alert.pool = strings.Repeat("x", 4096)
		if text := alert.format(); len(text) > 4096 || !utf8.ValidString(text) || strings.Contains(text, "<a") {
			t.Fatal("signal length limit must omit whole oversized metadata and links")
		}
		messages = nil
	}
}
