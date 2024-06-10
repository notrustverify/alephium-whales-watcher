package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
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
		Message        string `json:"message"`
		Spent          string `json:"spent"`
	} `json:"outputs"`
	GasAmount         int    `json:"gasAmount"`
	GasPrice          string `json:"gasPrice"`
	ScriptExecutionOk bool   `json:"scriptExecutionOk"`
	Coinbase          bool   `json:"coinbase"`
}

func getBlocksFullnode(apiClient *openapiclient.APIClient, ctx *context.Context, fromTs int64, toTs int64) {
	blocks, r, err := apiClient.BlockflowApi.GetBlockflowBlocks(*ctx).FromTs(fromTs).ToTs(toTs).Execute()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error when calling `BlockflowApi.GetBlockflowBlocks``: %v\n", err)
		fmt.Fprintf(os.Stderr, "Full HTTP response: %v\n", r)
	}

	for group := 0; group < len(blocks.Blocks); group++ {
		block := blocks.Blocks[group]
		for blockId := 0; blockId < len(blocks.Blocks[group]); blockId++ {
			for tx := 0; tx < len(block[blockId].Transactions); tx++ {
				if len(block[blockId].Transactions[tx].Unsigned.Inputs) > 0 {
					txId := block[blockId].Transactions[tx].Unsigned.TxId
					//getTxData(apiClient, ctx, txId)
					chTxs <- txId
				}
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
		log.Printf("Unknown error code from explorer: status code %d\n", statusCode)
		return false
	}

	if statusCode == 200 && len(dataBytes) > 0 {
		err := json.Unmarshal(dataBytes, &tx)
		if err != nil {
			log.Printf("Cannot unmarshall data, err: %s\n", err)
			panic(1)
		}
	}

	if strings.ToLower(tx.Type) == "accepted" {
		return true
	}

	return false

}

func getTxData(txId string) {
	var txData Transaction
	cntRetry := 0

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
					chMessages <- Message{addressIn, addressOut, hintAmountALPH, txId}
				}
			}
		}
	}
}
