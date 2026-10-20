package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"
)

func txCountTestBlock(t *testing.T, ids ...string) *Ws {
	t.Helper()
	txs := make([]string, 0, len(ids))
	for _, id := range ids {
		txs = append(txs, fmt.Sprintf(`{"unsigned":{"txId":%q,"inputs":[{}]}}`, id))
	}
	payload := fmt.Sprintf(`{"method":"subscription","params":{"result":{"block":{"hash":"block","height":10,"transactions":[%s]}}}}`, strings.Join(txs, ","))
	var block Ws
	if err := json.Unmarshal([]byte(payload), &block); err != nil {
		t.Fatal(err)
	}
	return &block
}

func TestTxCountThresholdAndRollingWindow(t *testing.T) {
	m := &txCountMonitor{threshold: 2, seen: make(map[string]struct{})}
	start := time.Unix(1000, 0)
	if _, notify := m.observeBlock(txCountTestBlock(t, "a", "b"), start); notify {
		t.Fatal("count equal to the threshold must not alert")
	}
	alert, notify := m.observeBlock(txCountTestBlock(t, "c", "d"), start.Add(time.Minute))
	if !notify || alert.count != 4 {
		t.Fatalf("expected one alert with the complete block count, got %+v, %v", alert, notify)
	}
	if _, notify := m.observeBlock(txCountTestBlock(t, "e"), start.Add(2*time.Minute)); notify {
		t.Fatal("remaining above the threshold must not repeat the alert")
	}
	// At exactly five minutes a and b expire, leaving c, d and e above
	// the threshold. The count is rolling, not reset every five minutes.
	if _, notify := m.observeBlock(txCountTestBlock(t), start.Add(txCountWindow)); notify || len(m.seen) != 3 {
		t.Fatalf("incorrect rolling expiry: count=%d notify=%v", len(m.seen), notify)
	}
	// c and d now expire; f and g take the count from 1 to 3 and rearm it.
	alert, notify = m.observeBlock(txCountTestBlock(t, "f", "g"), start.Add(6*time.Minute))
	if !notify || alert.count != 3 {
		t.Fatalf("expected a new crossing after expiry, got %+v, %v", alert, notify)
	}
	if _, notify := m.observeBlock(txCountTestBlock(t), start.Add(12*time.Minute)); notify || len(m.seen) != 0 || len(m.transactions) != 0 || m.above {
		t.Fatal("idle expiry must clear the count and rearm alerts")
	}
}

func TestTxCountExcludesCoinbaseAndDuplicates(t *testing.T) {
	m := &txCountMonitor{threshold: 1, seen: make(map[string]struct{})}
	start := time.Unix(1000, 0)
	block := txCountTestBlock(t, "coinbase", "a", "a", "")
	block.Params.Result.Block.Transactions[0].Unsigned.Inputs = nil
	if _, notify := m.observeBlock(block, start); notify || len(m.seen) != 1 {
		t.Fatal("coinbase, duplicate and empty transaction IDs must not inflate the count")
	}
	block.Params.Result.Block.ChainFrom = 3
	block.Params.Result.Block.ChainTo = 2
	if _, notify := m.observeBlock(block, start.Add(time.Minute)); notify || len(m.seen) != 1 {
		t.Fatal("replayed transactions from other chain groups must not be counted twice")
	}
	if alert, notify := m.observeBlock(txCountTestBlock(t, "b"), start.Add(2*time.Minute)); !notify || alert.count != 2 {
		t.Fatal("transactions across chain groups should share a count")
	}
	block.Method = ""
	if _, notify := m.observeBlock(block, start.Add(time.Hour)); notify || len(m.seen) != 2 {
		t.Fatal("non-subscription messages must not change the count")
	}
}

func TestTxCountConcurrentBlocks(t *testing.T) {
	alerts := make(chan txCountAlert, 20)
	m := &txCountMonitor{threshold: 2, seen: make(map[string]struct{}), alerts: alerts}
	block := txCountTestBlock(t, "a", "b", "c")
	var workers sync.WaitGroup
	for i := 0; i < 20; i++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			m.recordBlock(block)
		}()
	}
	workers.Wait()
	if len(alerts) != 1 || len(m.seen) != 3 {
		t.Fatalf("concurrent replay produced %d alerts and %d transactions", len(alerts), len(m.seen))
	}
	for len(alerts) > 0 {
		<-alerts
		notificationQueueMetric.Dec()
	}
}

func TestTxCountConfiguration(t *testing.T) {
	for _, value := range []string{"", "0", "-1", "abc", "1.5", "99999999999999999999999999"} {
		t.Run("disabled_"+value, func(t *testing.T) {
			t.Setenv("TX_COUNT_THRESHOLD", value)
			if initTxCountMonitor(nil) != nil {
				t.Fatal("missing, zero or invalid threshold must disable alerts")
			}
		})
	}
	t.Setenv("TX_COUNT_THRESHOLD", " 100 ")
	if m := initTxCountMonitor(nil); m == nil || m.threshold != 100 {
		t.Fatal("positive threshold should enable alerts")
	}
}

func TestTxCountOnlyRecordsConfirmedMainChainBlocks(t *testing.T) {
	oldURL, oldMonitor := parameters.FullnodeApi, transactionCountMonitor
	t.Cleanup(func() {
		parameters.FullnodeApi, transactionCountMonitor = oldURL, oldMonitor
	})
	var mainChain atomic.Value
	mainChain.Store("false")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/blockflow/chain-info":
			fmt.Fprint(w, `{"currentHeight":20}`)
		case "/blockflow/is-block-in-main-chain":
			fmt.Fprint(w, mainChain.Load())
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	parameters.FullnodeApi = server.URL
	alerts := make(chan txCountAlert, 2)
	transactionCountMonitor = &txCountMonitor{threshold: 1, seen: make(map[string]struct{}), alerts: alerts}
	txs := make(chan Tx, 4)
	block := txCountTestBlock(t, "a", "b")
	getTxIdWs(block, txs)
	if len(alerts) != 0 || len(txs) != 0 || len(transactionCountMonitor.seen) != 0 {
		t.Fatal("ghost blocks must not contribute to transaction count alerts")
	}
	mainChain.Store("true")
	getTxIdWs(block, txs)
	if len(alerts) != 1 || len(txs) != 2 {
		t.Fatalf("confirmed block: got %d alerts and %d queued transactions", len(alerts), len(txs))
	}
	notificationQueueMetric.Dec()
	mainChain.Store("invalid response")
	getTxIdWs(txCountTestBlock(t, "c", "d"), txs)
	if len(alerts) != 1 || len(transactionCountMonitor.seen) != 2 {
		t.Fatal("unverified main-chain status must not contribute to count alerts")
	}
	txQueueMetrics.Sub(float64(len(txs)))
}

func TestTxCountAlertMessage(t *testing.T) {
	alert := txCountAlert{count: 101}
	want := "🚨 High transaction activity\n\n101 confirmed transactions observed in the last 5 minutes."
	if got := alert.format(true); got != want {
		t.Fatalf("unexpected message without blocks: %q", got)
	}
	for i := 0; i < 4; i++ {
		alert.blocks = append(alert.blocks, txCountBlock{hash: fmt.Sprintf("%064d", i), height: 1234567 - i, groupFrom: i, groupTo: 0})
	}
	telegram, twitter := alert.format(true), alert.format(false)
	for _, text := range []string{telegram, twitter} {
		if strings.Contains(text, "threshold") || strings.Contains(text, "Alephium") || strings.Contains(text, "/#/") || !strings.Contains(text, "across 4 blocks.") || !strings.Contains(text, txCountBlockExplorerURL+alert.blocks[0].hash) {
			t.Fatalf("unexpected message: %s", text)
		}
		if strings.Contains(text, alert.blocks[3].hash) {
			t.Fatal("too many block links")
		}
	}
	if strings.Count(telegram, "<a href=") != 3 || !strings.Contains(telegram, fmt.Sprintf(`<a href="%s%s">#1234567</a> (0 → 0)`, txCountBlockExplorerURL, alert.blocks[0].hash)) {
		t.Fatalf("incorrect Telegram block links: %s", telegram)
	}
	if strings.Contains(twitter, "<a") || strings.Count(twitter, "/blocks/") != 1 || utf8.RuneCountInString(twitter) > 280 {
		t.Fatalf("incorrect Twitter block links: %s", twitter)
	}
	alert.blocks[0].hash = `hash"&test`
	if !strings.Contains(alert.format(true), "hash&#34;&amp;test") {
		t.Fatal("Telegram links must escape HTML")
	}
}

func TestTxCountTelegramLengthLimitPreservesLinks(t *testing.T) {
	alert := txCountAlert{count: 101, blocks: []txCountBlock{
		{hash: strings.Repeat("a", 2000), height: 1},
		{hash: strings.Repeat("b", 2000), height: 2},
		{hash: "third", height: 3},
	}}
	text := alert.format(true)
	if len(text) > txCountTelegramMaxMessageBytes || !utf8.ValidString(text) {
		t.Fatal("Telegram message exceeds its limit or contains broken UTF-8")
	}
	if strings.Count(text, "<a href=") != 1 || strings.Count(text, "</a>") != 1 || !strings.Contains(text, ">#1</a>") || strings.Contains(text, ">#2") {
		t.Fatalf("length limiting must preserve whole links: %s", text)
	}
	alert.blocks[0].hash = strings.Repeat("🚨", txCountTelegramMaxMessageBytes)
	text = alert.format(true)
	if len(text) > txCountTelegramMaxMessageBytes || strings.Contains(text, "Recent blocks:") || strings.Contains(text, "<a") {
		t.Fatal("an oversized first link should be omitted with its heading")
	}
}

func TestTxCountBlockReferencesFollowRollingWindow(t *testing.T) {
	m := &txCountMonitor{threshold: 1, seen: make(map[string]struct{})}
	start := time.Unix(1000, 0)
	first := txCountTestBlock(t, "a")
	first.Params.Result.Block.Hash = "first"
	second := txCountTestBlock(t, "b", "c")
	second.Params.Result.Block.Hash = "second"
	second.Params.Result.Block.Height = 12
	second.Params.Result.Block.ChainFrom = 2
	m.observeBlock(first, start)
	alert, notify := m.observeBlock(second, start.Add(time.Minute))
	if !notify || len(alert.blocks) != 2 || alert.blocks[0].hash != "second" || alert.blocks[1].hash != "first" || alert.blocks[0].height != 12 || alert.blocks[0].groupFrom != 2 {
		t.Fatalf("incorrect contributing blocks: %+v", alert.blocks)
	}
	third := txCountTestBlock(t, "d", "e")
	third.Params.Result.Block.Hash = "third"
	alert, notify = m.observeBlock(third, start.Add(7*time.Minute))
	if !notify || len(alert.blocks) != 1 || alert.blocks[0].hash != "third" {
		t.Fatalf("expired blocks included in new alert: %+v", alert.blocks)
	}
	if !strings.Contains(alert.format(true), "across 1 block.") {
		t.Fatal("single block should use singular wording")
	}
}
