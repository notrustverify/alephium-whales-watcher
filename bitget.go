package main

import (
	"encoding/json"
	"fmt"
	"log"
	"strconv"
)

type BitgetAggTrades struct {
	Code        string `json:"code"`
	Msg         string `json:"msg"`
	RequestTime int64  `json:"requestTime"`
	Data        []struct {
		Symbol  string `json:"symbol"`
		TradeID string `json:"tradeId"`
		Side    string `json:"side"`
		Price   string `json:"price"`
		Size    string `json:"size"`
		Ts      string `json:"ts"`
	} `json:"data"`
}

func getTradesBitget(from int64, to int64, chMessagesCex chan MessageCex) {

	var trades BitgetAggTrades
	dataBytes, _, err := getHttp(fmt.Sprintf("https://api.bitget.com/api/v2/spot/market/fills-history?symbol=ALPHUSDT&limit=1000&startTime=%d&endTime=%d", from, to))
	if err != nil {
		log.Printf("Error getting price\n%s\n", err)
		return
	}

	if len(dataBytes) > 0 {
		json.Unmarshal(dataBytes, &trades)
	}

	for _, v := range trades.Data {
		amountToFloat, err := strconv.ParseFloat(v.Size, 64)
		if err != nil {
			log.Printf("Cannot convert bitget amount, err: %s\n", err)
		}

		priceToFloat, err := strconv.ParseFloat(v.Price, 64)
		if err != nil {
			log.Printf("Cannot convert bitget price, err: %s\n", err)
		}

		tsToFloat, err := strconv.ParseInt(v.Ts, 10, 64)
		if err != nil {
			log.Printf("Cannot convert bitget price, err: %s\n", err)
		}

		if amountToFloat >= parameters.MinAmountTrigger && tsToFloat >= from && tsToFloat <= to {
			chMessagesCex <- MessageCex{v.Side, amountToFloat, amountToFloat * priceToFloat, "Bitget", priceToFloat}
		}
	}

}
