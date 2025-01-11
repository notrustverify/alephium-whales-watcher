package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
)

type MexcAggTrades []struct {
	ID           interface{} `json:"id"`
	Price        string      `json:"price"`
	Qty          string      `json:"qty"`
	QuoteQty     string      `json:"quoteQty"`
	Time         int64       `json:"time"`
	IsBuyerMaker bool        `json:"isBuyerMaker"`
	IsBestMatch  bool        `json:"isBestMatch"`
	TradeType    string      `json:"tradeType"`
}

func getTradesMexc(from int64, to int64, chMessagesCex chan MessageCex, symbols []string) {
	for _, symbol := range symbols {
		getTrades(from, to, chMessagesCex, symbol)
	}
}

func getTrades(from int64, to int64, chMessagesCex chan MessageCex, symbol string) {
	var trades MexcAggTrades
	dataBytes, _, err := getHttp(fmt.Sprintf("https://api.mexc.com/api/v3/trades/?symbol=%sUSDT", symbol))
	if err != nil {
		log.Printf("Error getting price\n%s\n", err)
		return
	}

	if len(dataBytes) > 0 {
		json.Unmarshal(dataBytes, &trades)
	}

	for _, v := range trades {
		amountToFloat, err := strconv.ParseFloat(v.Qty, 64)
		if err != nil {
			log.Printf("Cannot convert mexc amount, err: %s\n", err)
		}

		priceToFloat, err := strconv.ParseFloat(v.Price, 64)
		if err != nil {
			log.Printf("Cannot convert mexc price, err: %s\n", err)
		}

		quoteQtyToFloat, err := strconv.ParseFloat(v.QuoteQty, 64)
		if err != nil {
			log.Printf("Cannot convert mexc quote quantity, err: %s\n", err)
		}

		side := "buy"
		if v.IsBuyerMaker {
			side = "sell"
		}

		if quoteQtyToFloat >= parameters.MinAmountCexTriggerUsd && v.Time >= from && v.Time <= to {
			chMessagesCex <- MessageCex{side, Amount{amountToFloat, symbol}, Amount{quoteQtyToFloat, "USDT"}, "Mexc", priceToFloat}
			cexQueueMetrics.Inc()
		}
	}
}
