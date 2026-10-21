package main

import (
	"fmt"
	"html"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"time"
)

type dexSignalConfig struct {
	window                time.Duration
	repeatCount           int
	repeatMinUSD          float64
	imbalancePercent      float64
	imbalanceMinVolumeUSD float64
	imbalanceMinSwaps     int
}

func loadDexSignalConfig() dexSignalConfig {
	cfg := dexSignalConfig{window: 5 * time.Minute, repeatMinUSD: 5000, imbalanceMinVolumeUSD: 25000, imbalanceMinSwaps: 5}
	if value := strings.TrimSpace(os.Getenv("DEX_REPEATED_SWAP_COUNT")); value != "" {
		count, err := strconv.Atoi(value)
		if err != nil || count < 0 || count == 1 {
			log.Printf("invalid DEX_REPEATED_SWAP_COUNT %q; use 0 to disable or at least 2", value)
		} else {
			cfg.repeatCount = count
		}
	}
	if value := strings.TrimSpace(os.Getenv("DEX_REPEATED_SWAP_MIN_USD")); value != "" {
		minimum, err := strconv.ParseFloat(value, 64)
		if err != nil || minimum <= 0 || math.IsNaN(minimum) || math.IsInf(minimum, 0) {
			log.Printf("invalid DEX_REPEATED_SWAP_MIN_USD %q; repeated swap alerts disabled", value)
			cfg.repeatCount = 0
		} else {
			cfg.repeatMinUSD = minimum
		}
	}
	if value := strings.TrimSpace(os.Getenv("DEX_IMBALANCE_PERCENT")); value != "" {
		percent, err := strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(percent) || math.IsInf(percent, 0) || (percent != 0 && (percent <= 50 || percent > 100)) {
			log.Printf("invalid DEX_IMBALANCE_PERCENT %q; use 0 to disable or a percentage above 50 and at most 100", value)
		} else {
			cfg.imbalancePercent = percent
		}
	}
	if value := strings.TrimSpace(os.Getenv("DEX_IMBALANCE_MIN_VOLUME_USD")); value != "" {
		minimum, err := strconv.ParseFloat(value, 64)
		if err != nil || minimum <= 0 || math.IsNaN(minimum) || math.IsInf(minimum, 0) {
			log.Printf("invalid DEX_IMBALANCE_MIN_VOLUME_USD %q; imbalance alerts disabled", value)
			cfg.imbalancePercent = 0
		} else {
			cfg.imbalanceMinVolumeUSD = minimum
		}
	}
	if value := strings.TrimSpace(os.Getenv("DEX_IMBALANCE_MIN_SWAPS")); value != "" {
		count, err := strconv.Atoi(value)
		if err != nil || count < 2 {
			log.Printf("invalid DEX_IMBALANCE_MIN_SWAPS %q; imbalance alerts disabled", value)
			cfg.imbalancePercent = 0
		} else {
			cfg.imbalanceMinSwaps = count
		}
	}
	if value := strings.TrimSpace(os.Getenv("DEX_SIGNAL_WINDOW")); value != "" {
		window, err := time.ParseDuration(value)
		if err != nil || window <= 0 {
			log.Printf("invalid DEX_SIGNAL_WINDOW %q; DEX signal alerts disabled", value)
			cfg.repeatCount, cfg.imbalancePercent = 0, 0
		} else {
			cfg.window = window
		}
	}
	return cfg
}

type dexSignalAlert struct {
	pool       string
	quote      string
	kind       string
	side       string
	window     time.Duration
	count      int
	volumeUSD  float64
	buyUSD     float64
	sellUSD    float64
	share      float64
	minSwapUSD float64
}

func (a dexSignalAlert) format() string {
	side := "buys"
	if a.side == "sell" {
		side = "sells"
	}
	var text string
	if a.kind == "repeated" {
		text = fmt.Sprintf("🐋 Repeated large DEX swaps\n\n%d large ALPH %s in the last %s.\nEach swap: at least %s\nCombined estimated value: %s", a.count, side, formatTxCountWindow(a.window), (Amount{a.minSwapUSD, "USD"}).formatHuman(), (Amount{a.volumeUSD, "USD"}).formatHuman())
	} else {
		text = fmt.Sprintf("⚖️ DEX buy/sell imbalance\n\nALPH %s account for %.1f%% of estimated volume in the last %s.\nBuys: %s\nSells: %s\nSwaps: %d", side, a.share, formatTxCountWindow(a.window), (Amount{a.buyUSD, "USD"}).formatHuman(), (Amount{a.sellUSD, "USD"}).formatHuman(), a.count)
	}
	text = html.EscapeString(text)
	pair := html.EscapeString("\n\nPowfi pool: ALPH/" + a.quote)
	if len(text)+len(pair) <= txCountTelegramMaxMessageBytes {
		text += pair
	}
	link := fmt.Sprintf("\n<a href=\"https://explorer.alephium.org/addresses/%s\">View pool</a>", html.EscapeString(a.pool))
	if len(text)+len(link) <= txCountTelegramMaxMessageBytes {
		text += link
	}
	return text
}

func sendDexSignalAlert(alert dexSignalAlert) {
	chatID, err := privateTelegramChatID()
	if err != nil {
		log.Printf("%v; skipping private DEX signal alert", err)
		return
	}
	sendTelegramMessage(telegramBot, chatID, alert.format())
}

type observedDexSwap struct {
	id   string
	side int // 0 = ALPH buy, 1 = ALPH sell.
	usd  float64
	at   time.Time
}

type dexSignalSummary struct {
	counts       [2]int
	largeCounts  [2]int
	volumes      [2]float64
	largeVolumes [2]float64
}

// Each pool's polling goroutine owns its monitor. Windows use observation time,
// and transaction IDs are deduplicated per pool while inside the window.
type dexSignalMonitor struct {
	pool          string
	quote         string
	cfg           dexSignalConfig
	swaps         []observedDexSwap
	seen          map[string]bool
	repeatAbove   [2]bool
	imbalanceSide string
}

func (m *dexSignalMonitor) summarize() dexSignalSummary {
	var summary dexSignalSummary
	for _, swap := range m.swaps {
		summary.counts[swap.side]++
		summary.volumes[swap.side] += swap.usd
		if swap.usd >= m.cfg.repeatMinUSD {
			summary.largeCounts[swap.side]++
			summary.largeVolumes[swap.side] += swap.usd
		}
	}
	return summary
}

func (m *dexSignalMonitor) dominantSide(s dexSignalSummary) string {
	total := s.volumes[0] + s.volumes[1]
	if m.cfg.imbalancePercent == 0 || s.counts[0]+s.counts[1] < m.cfg.imbalanceMinSwaps || total < m.cfg.imbalanceMinVolumeUSD || total <= 0 || math.IsInf(total, 0) {
		return ""
	}
	if s.volumes[0]/total*100 >= m.cfg.imbalancePercent {
		return "buy"
	}
	if s.volumes[1]/total*100 >= m.cfg.imbalancePercent {
		return "sell"
	}
	return ""
}

func (m *dexSignalMonitor) observeBatch(messages []MessageCex, now time.Time) []dexSignalAlert {
	cutoff := now.Add(-m.cfg.window)
	expired := 0
	for expired < len(m.swaps) && !m.swaps[expired].at.After(cutoff) {
		delete(m.seen, m.swaps[expired].id)
		m.swaps[expired] = observedDexSwap{}
		expired++
	}
	m.swaps = m.swaps[expired:]
	before := m.summarize()
	for side := range m.repeatAbove {
		if before.largeCounts[side] < m.cfg.repeatCount {
			m.repeatAbove[side] = false
		}
	}
	if m.dominantSide(before) != m.imbalanceSide {
		m.imbalanceSide = ""
	}
	added := false
	for _, msg := range messages {
		if msg.ExchangeName != "Powfi" || msg.TxID == "" || m.seen[msg.TxID] || (msg.Side != "buy" && msg.Side != "sell") || (msg.AmountFiat.Symbol != "USD" && msg.AmountFiat.Symbol != "USDT") || msg.AmountFiat.Value <= 0 || math.IsNaN(msg.AmountFiat.Value) || math.IsInf(msg.AmountFiat.Value, 0) {
			continue
		}
		side := 0
		if msg.Side == "sell" {
			side = 1
		}
		if m.seen == nil {
			m.seen = make(map[string]bool)
		}
		m.seen[msg.TxID] = true
		m.swaps = append(m.swaps, observedDexSwap{id: msg.TxID, side: side, usd: msg.AmountFiat.Value, at: now})
		added = true
	}
	if !added {
		return nil // Expiry alone never generates a new activity alert.
	}
	summary := m.summarize()
	var alerts []dexSignalAlert
	for side, name := range []string{"buy", "sell"} {
		above := m.cfg.repeatCount > 0 && summary.largeCounts[side] >= m.cfg.repeatCount
		if above && !m.repeatAbove[side] && !math.IsInf(summary.largeVolumes[side], 0) {
			alerts = append(alerts, dexSignalAlert{pool: m.pool, quote: m.quote, kind: "repeated", side: name, window: m.cfg.window, count: summary.largeCounts[side], volumeUSD: summary.largeVolumes[side], minSwapUSD: m.cfg.repeatMinUSD})
		}
		m.repeatAbove[side] = above
	}
	dominant := m.dominantSide(summary)
	if dominant != "" && dominant != m.imbalanceSide {
		volume := summary.volumes[0]
		if dominant == "sell" {
			volume = summary.volumes[1]
		}
		alerts = append(alerts, dexSignalAlert{pool: m.pool, quote: m.quote, kind: "imbalance", side: dominant, window: m.cfg.window, count: summary.counts[0] + summary.counts[1], buyUSD: summary.volumes[0], sellUSD: summary.volumes[1], share: volume / (summary.volumes[0] + summary.volumes[1]) * 100})
	}
	m.imbalanceSide = dominant
	return alerts
}
