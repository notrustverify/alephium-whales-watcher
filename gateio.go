package main

import (
	"context"
	"log"
	"strconv"

	"github.com/antihax/optional"
	"github.com/gateio/gateapi-go/v6"
)

func getTradesGate(from int64, to int64, chMessagesCex chan MessageCex) {

	client := gateapi.NewAPIClient(gateapi.NewConfiguration())
	// uncomment the next line if your are testing against testnet
	// client.ChangeBasePath("https://fx-api-testnet.gateio.ws/api/v4")
	ctx := context.Background()
	currencyPair := "ALPH_USDT" // string - Currency pair

	result, _, err := client.SpotApi.ListTrades(ctx, currencyPair, &gateapi.ListTradesOpts{From: optional.NewInt64(from), To: optional.NewInt64(to), Limit: optional.NewInt32(1000)})
	if err != nil {
		if e, ok := err.(gateapi.GateAPIError); ok {
			log.Printf("gate api error: %s\n", e.Error())
			return
		} else {
			log.Printf("generic error: %s\n", err.Error())
			return
		}
	}

	for _, v := range result {
		amountToFloat, err := strconv.ParseFloat(v.Amount, 64)
		if err != nil {
			log.Printf("Cannot convert gateio amount, err: %s\n", err)
		}

		priceToFloat, err := strconv.ParseFloat(v.Price, 64)
		if err != nil {
			log.Printf("Cannot convert gateio price, err: %s\n", err)
		}

		if amountToFloat >= parameters.MinAmountTrigger {
			chMessagesCex <- MessageCex{v.Side, amountToFloat, amountToFloat * priceToFloat, "Gateio", priceToFloat}
		}
	}

}
