package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	openapiclient "github.com/alephium/go-sdk"
)

const maxRetry = 3600

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

type TokenList struct {
	NetworkID int `json:"networkId"`
	Tokens    []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Symbol      string `json:"symbol"`
		Decimals    int    `json:"decimals"`
		Description string `json:"description"`
		LogoURI     string `json:"logoURI"`
	} `json:"tokens"`
}

const maxRetryFullnode = 10

func getBlocksFullnode(apiClient *openapiclient.APIClient, ctx *context.Context, fromTs int64, toTs int64, ch chan string) {
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

	for group := 0; group < len(blocks.Blocks); group++ {
		block := blocks.Blocks[group]
		wg.Add(1)
		go getTxId(&block, &wg, ch)

	}
	wg.Wait()

}

func getTxId(block *[]openapiclient.BlockEntry, wg *sync.WaitGroup, chTxs chan string) {

	defer wg.Done()

	for _, txs := range *block {
		for _, tx := range txs.Transactions {
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
	for outputIndex := 0; outputIndex < len(txData.Outputs); outputIndex++ {
		addressOut = txData.Outputs[outputIndex].Address
		txType := txData.Outputs[outputIndex].Type

		if strings.ToLower(txType) == "contractoutput" {
			return
		}

		if strings.ToLower(txType) == "assetoutput" {
			attoStrToFloat, err := strconv.ParseFloat(txData.Outputs[outputIndex].AttoAlphAmount, 32)
			hintAmountALPH := attoStrToFloat / baseAlph

			if hintAmountALPH >= parameters.MinAmountTrigger {

				if addressIn != addressOut {

					if err != nil {
						fmt.Fprintf(os.Stderr, "Error when calling BlockflowApi.GetBlockflowBlocks: %v\n", err)
					}
					//log.Printf("From %s to %s -> %+v\n", addressIn, addressOut, attoStrToFloat/BASE_ALPH)
					chMessages <- Message{addressIn, addressOut, hintAmountALPH, txId, "ALPH"}
				}
			}

			if len(txData.Outputs[outputIndex].Tokens) > 0 {
				for _, token := range txData.Outputs[outputIndex].Tokens {
					if token.ID == "1a281053ba8601a658368594da034c2e99a0fb951b86498d05e76aedfe666800" {
						tokenAmount, err := strconv.ParseFloat(token.Amount, 64)
						if err != nil {
							log.Printf("Cannot parse ayin amount, err: %s\n", err)
							return
						}
						if tokenAmount/float64(1e18) >= parameters.MinAmountAyinTrigger {
							if addressIn != addressOut {
								chMessages <- Message{addressIn, addressOut, tokenAmount, txId, "AYIN"}
							}
						}
					}
				}
			}

		}
	}
}
