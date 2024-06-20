package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"math"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	openapiclient "github.com/alephium/go-sdk"
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

const maxRetryFullnode = 10
const maxRetry = 3600

// find transactions in each blocks
func getBlocksFullnode(apiClient *openapiclient.APIClient, ctx *context.Context, fromTs int64, toTs int64, ch chan string) {

	// limit number of retry when calling fullnode API
	cntRetry := 0
	var blocks *openapiclient.BlocksPerTimeStampRange

	for {
		blocksFullnode, r, err := apiClient.BlockflowApi.GetBlockflowBlocks(*ctx).FromTs(fromTs).ToTs(toTs).Execute()
		if err != nil {
			cntRetry++
		}

		if err == nil {
			blocks = blocksFullnode
			break
		}

		if cntRetry >= maxRetryFullnode {
			fmt.Fprintf(os.Stderr, "Error when calling `BlockflowApi.GetBlockflowBlocks``: %v\n", err)
			fmt.Fprintf(os.Stderr, "Full HTTP response: %v\n", r)
			panic(err)
		}
		time.Sleep(1 * time.Second)
	}

	wg := sync.WaitGroup{}

	for _, block := range blocks.Blocks {
		wg.Add(1)
		go getTxId(&block, &wg, ch)

	}
	wg.Wait()

}

func getTxId(block *[]openapiclient.BlockEntry, wg *sync.WaitGroup, chTxs chan string) {

	defer wg.Done()

	for _, txs := range *block {
		for _, tx := range txs.Transactions {

			// no input mean coinbase tx
			if len(tx.Unsigned.Inputs) > 0 {
				txId := tx.Unsigned.TxId

				chTxs <- txId
			}
		}

	}

}

func getTxStateExplorer(txId string, tx *Transaction) bool {
	dataBytes, statusCode, err := getHttp(fmt.Sprintf("%s/transactions/%s", parameters.ExplorerApi, txId))

	if err != nil { // do not print error if 404
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

func getTxData(txId string, chMessages chan Message, wId int) {
	var txData Transaction
	cntRetry := 0
	log.Printf("worker %d check %s\n", wId, txId)

	for {

		if getTxStateExplorer(txId, &txData) {
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
					chMessages <- Message{addressIn, addressOut, hintAmountALPH, txId, Token{}}
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
								chMessages <- Message{addressIn, addressOut, tokenAmount, txId, tokenData}
							}
						}
					}

				}
			}

		}
	}
}
