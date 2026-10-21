package main

import (
	"fmt"
	"html"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

const (
	defaultTxCountWindow           = 5 * time.Minute
	txCountBlockExplorerURL        = "https://explorer.alephium.org/blocks/"
	txCountTelegramMaxMessageBytes = 4096
)

var transactionCountMonitor *txCountMonitor

type txCountAlert struct {
	chatID int64
	count  int
	window time.Duration
	blocks []txCountBlock // Most recently observed first.
}

type txCountBlock struct {
	hash      string
	height    int
	groupFrom int
	groupTo   int
}

func (a txCountAlert) format(isTelegram bool) string {
	text := fmt.Sprintf("🚨 High transaction activity\n\n%d confirmed transactions observed in the last %s", a.count, formatTxCountWindow(a.window))
	if len(a.blocks) > 0 {
		unit := "blocks"
		if len(a.blocks) == 1 {
			unit = "block"
		}
		text += fmt.Sprintf(" across %d %s", len(a.blocks), unit)
	}
	text += "."
	limit := 1
	if isTelegram {
		limit = 3
	}
	if len(a.blocks) > 0 {
		for i, block := range a.blocks {
			if i >= limit {
				break
			}
			link := txCountBlockExplorerURL + block.hash
			prefix := ""
			if i == 0 {
				prefix = "\n\nRecent blocks:"
			}
			var line string
			if isTelegram {
				line = fmt.Sprintf("\n<a href=\"%s\">#%d</a> (%d → %d)", html.EscapeString(link), block.height, block.groupFrom, block.groupTo)
				// Bound the complete HTML in bytes, conservatively below Telegram's
				// character limit, and omit whole entries to preserve valid links.
				if len(text)+len(prefix)+len(line) > txCountTelegramMaxMessageBytes {
					break
				}
			} else {
				line = fmt.Sprintf("\n#%d (%d → %d)\n%s", block.height, block.groupFrom, block.groupTo, link)
			}
			text += prefix + line
		}
	}
	return text
}

func formatTxCountWindow(window time.Duration) string {
	for _, unit := range []struct {
		duration time.Duration
		name     string
	}{{time.Hour, "hour"}, {time.Minute, "minute"}, {time.Second, "second"}} {
		if window >= unit.duration && window%unit.duration == 0 {
			count := window / unit.duration
			name := unit.name
			if count != 1 {
				name += "s"
			}
			return fmt.Sprintf("%d %s", count, name)
		}
	}
	return window.String()
}

type countedTx struct {
	id    string
	at    time.Time
	block txCountBlock
}

type txCountMonitor struct {
	mu           sync.Mutex
	chatID       int64
	threshold    int
	window       time.Duration
	transactions []countedTx
	seen         map[string]struct{}
	above        bool
	alerts       chan<- txCountAlert
}

func initTxCountMonitor(alerts chan<- txCountAlert) *txCountMonitor {
	value := strings.TrimSpace(os.Getenv("TX_COUNT_THRESHOLD"))
	if value == "" {
		return nil
	}
	threshold, err := strconv.Atoi(value)
	if err != nil || threshold < 0 {
		log.Printf("invalid TX_COUNT_THRESHOLD %q; transaction count alerts disabled", value)
		return nil
	}
	if threshold == 0 {
		return nil
	}
	window := defaultTxCountWindow
	if value := strings.TrimSpace(os.Getenv("TX_COUNT_WINDOW")); value != "" {
		window, err = time.ParseDuration(value)
		if err != nil || window <= 0 {
			log.Printf("invalid TX_COUNT_WINDOW %q; use a positive duration such as 30s, 5m or 1h; transaction count alerts disabled", value)
			return nil
		}
	}
	chatID, err := privateTelegramChatID()
	if err != nil {
		log.Printf("%v; transaction count alerts disabled", err)
		return nil
	}
	log.Printf("Transaction count alerts enabled: more than %d transactions in %s", threshold, formatTxCountWindow(window))
	return &txCountMonitor{
		chatID:    chatID,
		threshold: threshold,
		window:    window,
		seen:      make(map[string]struct{}),
		alerts:    alerts,
	}
}

// Called only after the existing confirmation and main-chain checks. Serialize
// observation times across block workers so expiry stays ordered.
func (m *txCountMonitor) recordBlock(block *Ws) {
	m.mu.Lock()
	alert, notify := m.observeBlock(block, time.Now())
	m.mu.Unlock()
	if notify {
		notificationQueueMetric.Inc()
		m.alerts <- alert
	}
}

// observeBlock maintains the rolling interval (now - window, now]. The caller
// holds mu; keeping time explicit lets tests exercise expiry without sleeping.
func (m *txCountMonitor) observeBlock(block *Ws, now time.Time) (txCountAlert, bool) {
	if block.Method != subscriptionNotify {
		return txCountAlert{}, false
	}
	cutoff := now.Add(-m.window)
	expired := 0
	for expired < len(m.transactions) && !m.transactions[expired].at.After(cutoff) {
		delete(m.seen, m.transactions[expired].id)
		m.transactions[expired] = countedTx{}
		expired++
	}
	m.transactions = m.transactions[expired:]
	if len(m.seen) <= m.threshold {
		m.above = false
	}

	blockData := block.Params.Result.Block
	ref := txCountBlock{hash: blockData.Hash, height: blockData.Height, groupFrom: blockData.ChainFrom, groupTo: blockData.ChainTo}
	for _, tx := range blockData.Transactions {
		id := tx.Unsigned.TxID
		if id == "" || len(tx.Unsigned.Inputs) == 0 {
			continue
		}
		if _, exists := m.seen[id]; exists {
			continue
		}
		m.seen[id] = struct{}{}
		m.transactions = append(m.transactions, countedTx{id: id, at: now, block: ref})
	}
	if len(m.seen) > m.threshold && !m.above {
		m.above = true
		alert := txCountAlert{chatID: m.chatID, count: len(m.seen), window: m.window}
		seenBlocks := make(map[string]bool)
		for i := len(m.transactions) - 1; i >= 0; i-- {
			ref := m.transactions[i].block
			if ref.hash != "" && !seenBlocks[ref.hash] {
				seenBlocks[ref.hash] = true
				alert.blocks = append(alert.blocks, ref)
			}
		}
		return alert, true
	}
	return txCountAlert{}, false
}
