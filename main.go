package main

import (
	"fmt"
	"log"
	"math/rand"
	"time"

	"github.com/go-co-op/gocron"
	"github.com/michimani/gotwi"
	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
)

type Message struct {
	from        string
	to          string
	amountChain float64
	txId        string
	tokenData   Token
	groupFrom   int
	groupTo     int
}

type Tx struct {
	id        string
	height    int
	groupFrom int
	groupTo   int
}

type MessageCex struct {
	Side         string
	AmountLeft   Amount
	AmountFiat   Amount
	ExchangeName string
	Price        float64
}

type CexSymbol struct {
	cex    string
	symbol string
}

type Parameters struct {
	TelegramChatId           int64
	TelegramTokenApi         string
	TwitterAccessToken       string
	TwitterAccessTokenSecret string
	ExplorerApi              string
	FullnodeApi              string
	WsFullnode               string
	FrontendExplorerUrl      string
	MinAmountTrigger         float64
	MinAmountCexTriggerUsd   float64
	debugMode                bool
	PollingIntervalSec       int64
	KnownWalletUrl           string
	PriceUrl                 string
	TokenListUrl             string
}

var telegramBot *telego.Bot
var twitterBot *gotwi.Client
var KnownWallets map[string]KnownWallet
var Tokens TokenList

var parameters Parameters

func main() {

	loadEnv()
	loadTokensToTrack()

	updateTokens()
	updateKnownWallet()

	go metricsHttp()
	cronScheduler := gocron.NewScheduler(time.UTC)

	cronScheduler.Every("5m").Do(updatePrice)
	cronScheduler.Every("1h").Do(updateKnownWallet)
	cronScheduler.Every("1h").Do(updateTokens)
	cronScheduler.StartAsync()
	rand.NewSource(time.Now().UnixNano())

	maxWorkersTxs := 30
	txQueueWorkersMetrics.Set(float64(maxWorkersTxs))
	txQueueSize := 500
	txQueueSizeMetrics.Set(float64(txQueueSize))
	cexQueueSize := 100
	cexQueueSizeMetric.Set(float64(cexQueueSize))
	notificationQueueSize := 300
	notificationQueueSizeMetric.Set(float64(notificationQueueSize))

	chMessages := make(chan Message, notificationQueueSize)
	chMessagesCex := make(chan MessageCex, cexQueueSize)
	chTxs := make(chan Tx, txQueueSize)

	telegramBot = initTelegram()
	var err error
	twitterBot, err = initTwitter()
	if err != nil {
		log.Printf("cannot init twitter, err: %s\n", err)
		twitterBot = nil
	}

	for w := 1; w <= maxWorkersTxs; w++ {
		go checkTx(chTxs, chMessages, w)
		go messageConsumer(chMessagesCex, chMessages)
	}

	if parameters.debugMode {
		testsAlert(chTxs)
	}

	go getCexTrades(chMessagesCex)
	getBlocksFullnode(chTxs)

}

func checkTx(ch chan Tx, msgCh chan Message, wId int) {
	for {

		select {
		case tx := <-ch:
			getTxData(tx, msgCh, wId)
			fmt.Println(len(ch))
			txQueueMetrics.Dec()
		default:
			time.Sleep(500 * time.Millisecond)
		}
	}
}

func getCexTrades(msgCh chan MessageCex) {
	for {
		t := time.Now().Unix()
		mexcSymbols := []string{"ALPH", "ABX", "AYIN"}

		go getTradesGate(t-parameters.PollingIntervalSec, t, msgCh)
		go getTradesMexc(t*1000-parameters.PollingIntervalSec*1000, t*1000, msgCh, mexcSymbols)
		go getTradesBitget(t*1000-parameters.PollingIntervalSec*1000, t*1000, msgCh)

		log.Println("CEX - Sleepy sleepy")
		time.Sleep(time.Duration(parameters.PollingIntervalSec) * time.Second)
	}

}

func initTelegram() *telego.Bot {
	bot, err := telego.NewBot(parameters.TelegramTokenApi, telego.WithAPICaller(&ta.RetryCaller{
		// Use caller
		Caller: ta.DefaultFastHTTPCaller,
		// Max number of attempts to make call
		MaxAttempts: 15,
		// Exponent base for delay
		ExponentBase: 2,
		// Starting delay duration
		StartDelay: time.Millisecond * 45,
		// Maximum delay duration
		MaxDelay: time.Second * 120,
	}))
	if nil != err {
		// panics for the sake of simplicity.
		// you should handle this error properly in your code.
		panic(err)
	}

	return bot
}

func initTwitter() (*gotwi.Client, error) {
	in := &gotwi.NewClientInput{
		AuthenticationMethod: gotwi.AuthenMethodOAuth1UserContext,
		OAuthToken:           parameters.TwitterAccessToken,
		OAuthTokenSecret:     parameters.TwitterAccessTokenSecret,
	}

	return gotwi.NewClient(in)
}

func messageConsumer(chMessagesCex chan MessageCex, chMessages chan Message) {

	for {

		select {
		case msg := <-chMessagesCex:
			sendTelegramMessage(telegramBot, parameters.TelegramChatId, formatCexMessage(msg))

			if twitterBot != nil {
				sendTwitterPost(twitterBot, formatCexMessage(msg))
			}
			//formatCexMessage(<-chMessagesCex)
			cexQueueMetrics.Dec()
		case msg := <-chMessages:
			sendTelegramMessage(telegramBot, parameters.TelegramChatId, messageFormat(msg, true))

			if twitterBot != nil {
				sendTwitterPost(twitterBot, messageFormat(msg, false))
			}
			notificationQueueMetric.Dec()
		//telegramMessageFormat(<-chMessages)
		default:
			time.Sleep(500 * time.Millisecond)
		}

	}
}
