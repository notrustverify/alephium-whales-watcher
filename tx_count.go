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
	txCountWindow                  = 5 * time.Minute
	txCountBlockExplorerURL        = "https://explorer.alephium.org/blocks/"
	txCountTelegramMaxMessageBytes = 4096
)

var transactionCountMonitor *txCountMonitor

type txCountAlert struct {
	chatID int64
	count  int
	blocks []txCountBlock // Most recently observed first.
}

type txCountBlock struct {
	hash      string
	height    int
	groupFrom int
	groupTo   int
}

func (a txCountAlert) format(isTelegram bool) string {
	text := fmt.Sprintf("🚨 High transaction activity\n\n%d confirmed transactions observed in the last 5 minutes", a.count)
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

type countedTx struct {
	id    string
	at    time.Time
	block txCountBlock
}

type txCountMonitor struct {
	mu           sync.Mutex
	chatID       int64
	threshold    int
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
	chatID, err := strconv.ParseInt(strings.TrimSpace(os.Getenv("PRIVATE_CHAT_ID")), 10, 64)
	if err != nil || chatID == 0 {
		log.Print("PRIVATE_CHAT_ID must be a nonzero Telegram chat ID; transaction count alerts disabled")
		return nil
	}
	log.Printf("Transaction count alerts enabled: more than %d transactions in 5 minutes", threshold)
	return &txCountMonitor{
		chatID:    chatID,
		threshold: threshold,
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

// observeBlock maintains the rolling interval (now - 5 minutes, now]. The caller
// holds mu; keeping time explicit lets tests exercise expiry without sleeping.
func (m *txCountMonitor) observeBlock(block *Ws, now time.Time) (txCountAlert, bool) {
	if block.Method != subscriptionNotify {
		return txCountAlert{}, false
	}
	cutoff := now.Add(-txCountWindow)
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
		alert := txCountAlert{chatID: m.chatID, count: len(m.seen)}
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
