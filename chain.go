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
	block_notify Method = "block_notify"
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

type Ws struct {
	Method Method `json:"method"`
	Params struct {
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
	} `json:"params"`
	Jsonrpc string `json:"jsonrpc"`
}

const maxRetry = 3600

var done chan interface{}
var interrupt chan os.Signal

// find transactions in each blocks
func getBlocksFullnode(ch chan Tx) {

	//interrupt := make(chan os.Signal, 1)
	//signal.Notify(interrupt, os.Interrupt)

	u := url.URL{Scheme: "wss", Host: parameters.WsFullnode, Path: "/events"}
	done = make(chan interface{})    // Channel to indicate that the receiverHandler is done
	interrupt = make(chan os.Signal) // Channel to listen for interrupt signal to terminate gracefully

	signal.Notify(interrupt, os.Interrupt) // Notify the interrupt channel for SIGINT

	conn, _, err := websocket.DefaultDialer.Dial(u.String(), nil)
	if err != nil {
		log.Fatal("Error connecting to Websocket Server:", err)
	}
	defer conn.Close()
	go receiveHandler(conn, ch)

	for {
		select {
		case <-time.After(time.Duration(10) * time.Millisecond * 1000):
			// Send an echo packet every 10 second
			err := conn.WriteMessage(websocket.TextMessage, []byte("ping"))
			if err != nil {
				log.Println("Error during writing to websocket:", err)
				return
			}

		case <-interrupt:
			// We received a SIGINT (Ctrl + C). Terminate gracefully...
			log.Println("Received SIGINT interrupt signal. Closing all pending connections")

			// Close our websocket connection
			err := conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""))
			if err != nil {
				log.Println("Error during closing websocket:", err)
				return
			}

			select {
			case <-done:
				log.Println("Receiver Channel Closed! Exiting....")
			case <-time.After(time.Duration(1) * time.Second):
				log.Println("Timeout in closing receiving channel. Exiting....")
			}
			return
		}
	}

}

func receiveHandler(connection *websocket.Conn, ch chan Tx) {
	defer close(done)
	for {
		_, msg, err := connection.ReadMessage()
		if err != nil {
			log.Println("Error in receive:", err)
			return
		}
		//log.Printf("Received: %s\n", msg)
		var data Ws
		json.Unmarshal([]byte(msg), &data)
		getTxIdWs(&data, ch)
		//log.Printf("%+v", data)

	}
}

func getTxIdWs(block *Ws, chTxs chan Tx) {

	if block.Method == block_notify {

		isGhost, err := isGhostUncle(block.Params.Hash)
		if err != nil {
			log.Printf("Error checking if block is ghost uncle: %v", err)
		}

		if isGhost {
			log.Printf("Block %s is a ghost uncle.", block.Params.Hash)
		}

		for _, tx := range block.Params.Transactions {
			//fmt.Println(tx.Unsigned.TxID)
			// no input mean coinbase tx
			if len(tx.Unsigned.Inputs) > 0 {
				txId := Tx{id: tx.Unsigned.TxID, groupFrom: block.Params.ChainFrom, groupTo: block.Params.ChainTo}

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
		log.Printf("Error get data from explorer\n%s\n", err)
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
		time.Sleep(1 * time.Second)

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

		if strings.ToLower(txType) == "assetoutput" {
			attoStrToFloat, err := strconv.ParseFloat(output.AttoAlphAmount, 32)
			hintAmountALPH := attoStrToFloat / baseAlph

			if hintAmountALPH >= parameters.MinAmountTrigger {

				if addressIn != addressOut {

					if err != nil {
						fmt.Fprintf(os.Stderr, "Error when calling BlockflowApi.GetBlockflowBlocks: %v\n", err)
					}
					chMessages <- Message{addressIn, addressOut, hintAmountALPH, txId.id, Token{}, txId.groupFrom, txId.groupTo}
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
							}
						}
					}

				}
			}

		}
	}
}
