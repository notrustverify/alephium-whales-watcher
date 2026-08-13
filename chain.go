package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type Transaction struct {
	Type      string `json:"type"`
	Hash      string `json:"hash"`
	BlockHash string `json:"blockHash"`
	Timestamp int64  `json:"timestamp"`
	Inputs    []struct {
		OutputRef struct {
			Hint int    `json:"hint"`
			Key  string `json:"key"`
		} `json:"outputRef"`
		UnlockScript   string `json:"unlockScript"`
		TxHashRef      string `json:"txHashRef"`
		Address        string `json:"address"`
		AttoAlphAmount string `json:"attoAlphAmount"`
	} `json:"inputs"`
	Outputs []struct {
		Type           string `json:"type"`
		Hint           int    `json:"hint"`
		Key            string `json:"key"`
		AttoAlphAmount string `json:"attoAlphAmount"`
		Address        string `json:"address"`
		Tokens         []struct {
			ID     string `json:"id"`
			Amount string `json:"amount"`
		} `json:"tokens,omitempty"`
		Message string `json:"message"`
		Spent   string `json:"spent"`
	} `json:"outputs"`
	GasAmount         int    `json:"gasAmount"`
	GasPrice          string `json:"gasPrice"`
	ScriptExecutionOk bool   `json:"scriptExecutionOk"`
	Coinbase          bool   `json:"coinbase"`
}

type Method string

const (
	subscriptionNotify Method = "subscription"
)

type Token struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Symbol      string `json:"symbol"`
	Decimals    int    `json:"decimals"`
	Description string `json:"description"`
	LogoURI     string `json:"logoURI"`
}

type TokenList struct {
	NetworkID int     `json:"networkId"`
	Tokens    []Token `json:"tokens"`
}

type HeightResponse struct {
	CurrentHeight int `json:"currentHeight"`
}

type Ws struct {
	Method Method `json:"method"`
	Params struct {
		Subscription string `json:"subscription"`
		Result       struct {
			Block struct {
				Hash         string   `json:"hash"`
				Timestamp    int64    `json:"timestamp"`
				ChainFrom    int      `json:"chainFrom"`
				ChainTo      int      `json:"chainTo"`
				Height       int      `json:"height"`
				Deps         []string `json:"deps"`
				Transactions []struct {
					Unsigned struct {
						TxID         string `json:"txId"`
						Version      int    `json:"version"`
						NetworkID    int    `json:"networkId"`
						GasAmount    int    `json:"gasAmount"`
						GasPrice     string `json:"gasPrice"`
						Inputs       []any  `json:"inputs"`
						FixedOutputs []struct {
							Hint           int    `json:"hint"`
							Key            string `json:"key"`
							AttoAlphAmount string `json:"attoAlphAmount"`
							Address        string `json:"address"`
							Tokens         []any  `json:"tokens"`
							LockTime       int64  `json:"lockTime"`
							Message        string `json:"message"`
						} `json:"fixedOutputs"`
					} `json:"unsigned"`
					ScriptExecutionOk bool  `json:"scriptExecutionOk"`
					ContractInputs    []any `json:"contractInputs"`
					GeneratedOutputs  []any `json:"generatedOutputs"`
					InputSignatures   []any `json:"inputSignatures"`
					ScriptSignatures  []any `json:"scriptSignatures"`
				} `json:"transactions"`
				Nonce        string `json:"nonce"`
				Version      int    `json:"version"`
				DepStateHash string `json:"depStateHash"`
				TxsHash      string `json:"txsHash"`
				Target       string `json:"target"`
				GhostUncles  []any  `json:"ghostUncles"`
			} `json:"block"`
			Events []any `json:"events"`
		} `json:"result"`
	} `json:"params"`
	Jsonrpc string `json:"jsonrpc"`
}

type WsSubscribeRequest struct {
	Jsonrpc string   `json:"jsonrpc"`
	ID      int      `json:"id"`
	Method  string   `json:"method"`
	Params  []string `json:"params"`
}

const maxRetry = 3600

const (
	maxWorkers  = 50
	queueSize   = 500
	taskTimeout = 30 * time.Minute
)

type Task struct {
	data    *Ws
	ch      chan Tx
	retries int
}

type QueueMetrics struct {
	dropped   atomic.Int64
	queued    atomic.Int64
	processed atomic.Int64
}

type TaskQueue struct {
	tasks    chan Task
	workers  chan struct{}
	shutdown chan struct{}
	metrics  *QueueMetrics
}

func NewTaskQueue() *TaskQueue {
	tq := &TaskQueue{
		tasks:    make(chan Task, queueSize),
		workers:  make(chan struct{}, maxWorkers),
		shutdown: make(chan struct{}),
		metrics:  &QueueMetrics{},
	}

	// Start monitoring
	go tq.monitorQueue()

	// Start workers
	for i := 0; i < maxWorkers; i++ {
		go tq.worker()
	}
	workersMetrics.Set(float64(maxWorkers))
	queueSizeMetrics.Set(float64(queueSize))

	return tq
}

func (tq *TaskQueue) monitorQueue() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			log.Printf("Queue stats - Queued: %d, Processed: %d, Dropped: %d, Queue Size: %d/%d, Workers: %d/%d",
				tq.metrics.queued.Load(),
				tq.metrics.processed.Load(),
				tq.metrics.dropped.Load(),
				len(tq.tasks),
				cap(tq.tasks),
				len(tq.workers),
				cap(tq.workers))
			queuedMetrics.Set(float64(tq.metrics.processed.Load()))
			inqueueMetrics.Set(float64(len(tq.tasks)))
		case <-tq.shutdown:
			return
		}
	}
}

func (tq *TaskQueue) worker() {
	for {
		select {
		case <-tq.shutdown:
			return
		case task := <-tq.tasks:
			select {
			case tq.workers <- struct{}{}:
				getTxIdWs(task.data, task.ch)
				<-tq.workers // Release worker
				tq.metrics.processed.Add(1)
				processedMetrics.Inc()
			default:
				// Worker pool full - retry task
				time.Sleep(time.Duration(task.retries*100) * time.Millisecond)
				task.retries++
				retriedTasksMetrics.Inc()
				// Put task back in queue
				go func() {
					tq.tasks <- task
				}()
				log.Printf("Retrying task, attempt %d", task.retries)
			}
		}
	}
}

var taskQueue = NewTaskQueue()

var interrupt chan os.Signal

const (
	wsReadTimeout    = 60 * time.Second
	wsPingInterval   = 20 * time.Second
	wsInitialBackoff = 1 * time.Second
	wsMaxBackoff     = 60 * time.Second
)

var ignoredAddressPairs = map[string]string{
	"18KQPq3dJ9W4kXLWmtfMsRsptMRpkXe4HQCbRwXpw93jk": "12T7yHLpB1kaMBdHSApYM7H8aGXAET55axMiijJZYtK5G",
	"12T7yHLpB1kaMBdHSApYM7H8aGXAET55axMiijJZYtK5G": "15AG4h7gy9EThb1riPwzzZh5v1yvPJwJ2ZaYieVJ4e1YE",
	"1ANu47GYWwprmQJUgPpBsYb1mDoqxTDyVkCSg2C4NbtDp": "18KQPq3dJ9W4kXLWmtfMsRsptMRpkXe4HQCbRwXpw93jk",
	"15AG4h7gy9EThb1riPwzzZh5v1yvPJwJ2ZaYieVJ4e1YE": "1ANu47GYWwprmQJUgPpBsYb1mDoqxTDyVkCSg2C4NbtDp",
	"1DEmoThKNJ8KTwsBU8snPTjF7e9AG7fUbh9uaNemGwREp": "17R6Ptkz9i1LhiKyMhnitUMkgFygGeeQUFZvRx6GgV8Fc",
	"141Sf75o3SxyskgdHCsBmiW2AXqk5r2v3oqCC9bhbMdBd": "1DEmoThKNJ8KTwsBU8snPTjF7e9AG7fUbh9uaNemGwREp",
}

// find transactions in each blocks
// Runs a reconnect loop: any dial failure, subscribe failure, or read error/timeout
// (including a silently-dead TCP connection with no incoming data) triggers a fresh
// connection after a backoff, instead of leaving the process stuck forever with no logs.
func getBlocksFullnode(ch chan Tx) {

	u := url.URL{Scheme: "wss", Host: parameters.WsFullnode, Path: "/events"}
	interrupt = make(chan os.Signal, 1) // Channel to listen for interrupt signal to terminate gracefully

	signal.Notify(interrupt, os.Interrupt) // Notify the interrupt channel for SIGINT

	backoff := wsInitialBackoff

	for {
		select {
		case <-interrupt:
			log.Println("Received SIGINT interrupt signal. Exiting websocket loop")
			return
		default:
		}

		log.Printf("Connecting to fullnode websocket %s\n", u.String())
		conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
		if err != nil {
			log.Printf("Error connecting to Websocket Server: %v. Retrying in %s\n", err, backoff)
			if !sleepOrInterrupt(backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		subscribeReq := WsSubscribeRequest{
			Jsonrpc: "2.0",
			ID:      1,
			Method:  "subscribe",
			Params:  []string{"block"},
		}
		if err := conn.WriteJSON(subscribeReq); err != nil {
			log.Printf("Error subscribing to block notifications: %v. Reconnecting in %s\n", err, backoff)
			conn.Close()
			if !sleepOrInterrupt(backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		log.Println("Connected and subscribed to block notifications")
		backoff = wsInitialBackoff

		pingDone := make(chan struct{})
		go wsPingLoop(conn, pingDone)

		// receiveHandler blocks until the connection errors out, times out
		// (see wsReadTimeout / ping keepalive below), or is closed by us.
		receiveHandler(conn, ch)

		close(pingDone)
		conn.Close()

		select {
		case <-interrupt:
			log.Println("Received SIGINT interrupt signal. Exiting websocket loop")
			return
		default:
			log.Printf("Websocket disconnected, reconnecting in %s\n", backoff)
			if !sleepOrInterrupt(backoff) {
				return
			}
			backoff = nextBackoff(backoff)
		}
	}
}

func nextBackoff(backoff time.Duration) time.Duration {
	backoff *= 2
	if backoff > wsMaxBackoff {
		return wsMaxBackoff
	}
	return backoff
}

// sleepOrInterrupt waits out the backoff, returning false early if a SIGINT arrives.
func sleepOrInterrupt(d time.Duration) bool {
	select {
	case <-interrupt:
		return false
	case <-time.After(d):
		return true
	}
}

// wsPingLoop sends periodic ping frames so a silently-dead connection (no FIN/RST,
// just a black hole) hits the read deadline in receiveHandler instead of hanging forever.
func wsPingLoop(conn *websocket.Conn, done chan struct{}) {
	ticker := time.NewTicker(wsPingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ticker.C:
			if err := conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(5*time.Second)); err != nil {
				log.Printf("Error sending websocket ping: %v\n", err)
				return
			}
		case <-done:
			return
		}
	}
}

func receiveHandler(connection *websocket.Conn, ch chan Tx) {
	connection.SetPongHandler(func(string) error {
		connection.SetReadDeadline(time.Now().Add(wsReadTimeout))
		return nil
	})
	connection.SetReadDeadline(time.Now().Add(wsReadTimeout))

	for {
		_, msg, err := connection.ReadMessage()
		if err != nil {
			log.Printf("Error reading from websocket (connection considered dead): %v\n", err)
			return
		}
		connection.SetReadDeadline(time.Now().Add(wsReadTimeout))

		var data Ws
		if err := json.Unmarshal(msg, &data); err != nil {
			log.Printf("Error unmarshaling message: %v, raw: %s\n", err, string(msg))
			continue
		}

		task := Task{
			data:    &data,
			ch:      ch,
			retries: 0,
		}

		// Keep trying to queue task with exponential backoff
		go func(t Task) {
			backoff := time.Millisecond * 100
			for {
				select {
				case taskQueue.tasks <- t:
					taskQueue.metrics.queued.Add(1)
					queuedMetrics.Inc()
					return
				default:
					time.Sleep(backoff)
					backoff *= 2
					if backoff > time.Second*10 {
						backoff = time.Second * 10
					}
				}
			}
		}(task)
	}
}

func getTxIdWs(block *Ws, chTxs chan Tx) {
	if block.Method == subscriptionNotify {
		blockData := block.Params.Result.Block

		cntRetry := 0
		for {
			if getHeightFullnodeState(blockData.ChainFrom, blockData.ChainTo, blockData.Height) {
				isGhost, err := isGhostUncle(blockData.Hash)
				//log.Printf("Block %s is ghost uncle: %v", blockData.Hash, isGhost)
				if err != nil {
					log.Printf("Error checking if block %s is ghost uncle: %v", blockData.Hash, err)
				}

				if isGhost {
					log.Printf("Block %s is a ghost uncle.", blockData.Hash)
					return
				}

				break
			}

			if cntRetry >= maxRetry {
				log.Printf("Giving up on block %s (height %d, group %d->%d) after %d retries waiting for confirmations\n", blockData.Hash, blockData.Height, blockData.ChainFrom, blockData.ChainTo, cntRetry)
				return
			}

			cntRetry++
			time.Sleep(10 * time.Second)
		}

		for _, tx := range blockData.Transactions {

			// no input mean coinbase tx
			if len(tx.Unsigned.Inputs) > 0 {
				txId := Tx{id: tx.Unsigned.TxID, groupFrom: blockData.ChainFrom, groupTo: blockData.ChainTo, height: blockData.Height}

				txQueueMetrics.Inc()
				chTxs <- txId
			}
		}

	}
}

func isGhostUncle(blockHash string) (bool, error) {
	url := fmt.Sprintf("https://%s/blockflow/is-block-in-main-chain?blockHash=%s", parameters.FullnodeApi, blockHash)

	dataBytes, statusCode, err := getHttp(url)
	if err != nil {
		return false, fmt.Errorf("failed to query block status: %w", err)
	}

	if statusCode != 200 {
		return false, fmt.Errorf("unexpected status code: %d", statusCode)
	}

	var isMainChain bool
	err = json.Unmarshal(dataBytes, &isMainChain)
	if err != nil {
		return false, fmt.Errorf("failed to parse response: %w", err)
	}

	// Return inverse since we want to know if it's a ghost/uncle
	return !isMainChain, nil
}

func getTxStateExplorer(txId string, tx *Transaction) bool {
	dataBytes, statusCode, err := getHttp(fmt.Sprintf("%s/transactions/%s", parameters.ExplorerApi, txId))

	if err != nil && parameters.debugMode { // do not print error if 404
		//log.Printf("Error get data from explorer\n%s\n", err)
		return false
	}

	if statusCode != 404 && statusCode != 200 {
		fmt.Fprintf(os.Stderr, "Error fullnode explorer getting status: %v\n", err)
		fmt.Fprintf(os.Stderr, "Status code: %d\n", statusCode)
		panic(err)
	}

	if statusCode == 200 && len(dataBytes) > 0 {
		err := json.Unmarshal(dataBytes, &tx)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Cannot unmarshall data, err: %s\n", err)
			panic(err)
		}
	}

	if strings.ToLower(tx.Type) == "accepted" {
		return true
	}

	return false

}

func getHeightFullnodeState(groupFrom int, groupTo int, txHeight int) bool {
	url := fmt.Sprintf("https://%s/blockflow/chain-info?fromGroup=%d&toGroup=%d", parameters.FullnodeApi, groupFrom, groupTo)
	dataBytes, statusCode, err := getHttp(url)
	if err != nil {
		log.Printf("Error getting height for group %d->%d: %s\n", groupFrom, groupTo, err)
		return false
	}

	if statusCode != 200 {
		log.Printf("Error getting height for group %d->%d: unexpected status code %d\n", groupFrom, groupTo, statusCode)
		return false
	}

	var heightResp HeightResponse
	err = json.Unmarshal(dataBytes, &heightResp)
	if err != nil {
		log.Printf("Error unmarshaling height response for group %d->%d: %s, raw: %s\n", groupFrom, groupTo, err, string(dataBytes))
		return false
	}

	//log.Printf("block %d,now Height %d\n", txHeight, heightResp.CurrentHeight)
	//log.Println(heightResp.CurrentHeight - txHeight)
	return heightResp.CurrentHeight-txHeight >= 10
}

func getTxData(txId Tx, chMessages chan Message, wId int) {
	var txData Transaction
	cntRetry := 0
	log.Printf("worker %d check %s\n", wId, txId.id)

	for {

		if getTxStateExplorer(txId.id, &txData) {

			break
		}

		if cntRetry >= maxRetry {
			return
		}

		cntRetry++
		time.Sleep(10 * time.Second)

	}

	//log.Printf("Input %+v\n", txData)
	addressIn := txData.Inputs[0].Address

	var addressOut string
	for _, output := range txData.Outputs {
		addressOut = output.Address
		txType := output.Type

		if strings.ToLower(txType) == "contractoutput" {
			return
		}

		// ignore transactions between these pairs of addresses
		if expectedOut, exists := ignoredAddressPairs[addressIn]; exists && expectedOut == addressOut {
			return
		}

		if strings.ToLower(txType) == "assetoutput" {
			attoStrToFloat, err := strconv.ParseFloat(output.AttoAlphAmount, 32)
			hintAmountALPH := attoStrToFloat / baseAlph

			if hintAmountALPH >= parameters.MinAmountTrigger {

				if addressIn != addressOut {

					if err != nil {
						fmt.Fprintf(os.Stderr, "Error when calling BlockflowApi.GetBlockflowBlocks: %v\n", err)
					}
					chMessages <- Message{addressIn, addressOut, hintAmountALPH, txId.id, Token{}, txId.groupFrom, txId.groupTo}
					notificationQueueMetric.Inc()
				}
			}

			if len(output.Tokens) > 0 {
				for _, token := range output.Tokens {

					if amountTrigger, found := trackTokens[token.ID]; found {
						tokenData := searchTokenData(token.ID)
						if tokenData.Name == "" {
							log.Printf("error cannot found info for token %s", token.ID)
						}

						tokenAmount, err := strconv.ParseFloat(token.Amount, 64)
						if err != nil {
							log.Printf("Cannot parse ayin amount, err: %s\n", err)
							return
						}

						decimal := float64(tokenData.Decimals)
						amount := tokenAmount / math.Pow(10.0, decimal)

						if amount >= float64(amountTrigger) {
							if addressIn != addressOut {
								chMessages <- Message{addressIn, addressOut, tokenAmount, txId.id, tokenData, txId.groupFrom, txId.groupTo}
								notificationQueueMetric.Inc()
							}
						}
					}

				}
			}

		}
	}

}
