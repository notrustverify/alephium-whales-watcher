package main

import (
	"context"
	"log"
	"math/rand"
	"time"

	openapiclient "github.com/alephium/go-sdk"
	"github.com/go-co-op/gocron"
	"github.com/michimani/gotwi"
	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
)

type Message struct {
	from   string
	to     string
	amount float64
	txId   string
}

type MessageCex struct {
	Side         string
	Amount       float64
	AmountFiat   float64
	ExchangeName string
	Price        float64
}

type Parameters struct {
	TelegramChatId           int64
	TelegramTokenApi         string
	TwitterAccessToken       string
	TwitterAccessTokenSecret string
	ExplorerApi              string
	FullnodeApi              string
	FrontendExplorerUrl      string
	MinAmountTrigger         float64
	PollingIntervalSec       int64
	KnownWalletUrl           string
	PriceUrl                 string
}

var chMessages chan Message
var chMessagesCex chan MessageCex

var chTxs chan string

var telegramBot *telego.Bot
var twitterBot *gotwi.Client
var KnownWallets []KnownWallet
var parameters Parameters

func main() {

	loadEnv()

	cronScheduler := gocron.NewScheduler(time.UTC)

	configuration := openapiclient.NewConfiguration()
	apiClient := openapiclient.NewAPIClient(configuration)

	ctxAlephium := context.WithValue(context.Background(), openapiclient.ContextServerVariables,
		map[string]string{
			"protocol": "https",
			"host":     parameters.FullnodeApi,
			"port":     "443",
		})

	cronScheduler.Every("5m").Do(updatePrice)
	cronScheduler.Every("1h").Do(updateKnownWallet)
	cronScheduler.StartAsync()
	rand.NewSource(time.Now().UnixNano())
	getRndArticles()

	chMessages = make(chan Message)
	chMessagesCex = make(chan MessageCex)
	chTxs = make(chan string)

	updateKnownWallet()
	//testWallet := []KnownWallet{{Address: "1iAFqJZm6PMTUDquiV7MtDse6oHBxRcdsq2N3qzsSZ9Q", Name: "test"}}
	//KnownWallets = append(KnownWallets, testWallet...)

	//ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	//telegramBot := initTelegram()
	//defer cancel()
	//telegramBot.Start(ctx)

	go consumerChain()
	go consumerCex()
	go checkTx()

	telegramBot = initTelegram()
	var err error
	twitterBot, err = initTwitter()
	if err != nil {
		log.Printf("cannot init twitter, err: %s\n", err)
		twitterBot = nil
	}

	//log.Printf("%+v\n", knownWallets)

	//go getCexTrades()
	//getTxData(apiClient, &ctxAlephium, "777100496c124eb7b354285750b8ed8746ecc877de58c112fa23b721731e7c08")
	//getTxData(apiClient, &ctxAlephium, "d317add70567414626b6d7e5fd26e841cf5d81de6e2adb8e1a6d6968f47848ba")

	for {
		t := time.Now().Unix()
		getBlocksFullnode(apiClient, &ctxAlephium, t*1000-parameters.PollingIntervalSec*1000, t*1000)
		log.Println("Sleepy sleepy")
		time.Sleep(time.Duration(parameters.PollingIntervalSec) * time.Second)

	}

}

func checkTx() {
	for {
		tx := <-chTxs
		getTxData(tx)
	}
}

func getCexTrades() {
	for {
		t := time.Now().Unix()

		getTradesGate(t-parameters.PollingIntervalSec, t)
		getTradesMexc(t*1000-parameters.PollingIntervalSec*1000, t*1000)
		getTradesBitget(t*1000-parameters.PollingIntervalSec*1000, t*1000)
		log.Println("CEX - Sleepy sleepy")
		time.Sleep(time.Duration(parameters.PollingIntervalSec) * time.Second)
	}

}

func initTelegram() *telego.Bot {
	bot, err := telego.NewBot(parameters.TelegramTokenApi, telego.WithAPICaller(&ta.RetryCaller{
		// Use caller
		Caller: ta.DefaultFastHTTPCaller,
		// Max number of attempts to make call
		MaxAttempts: 4,
		// Exponent base for delay
		ExponentBase: 2,
		// Starting delay duration
		StartDelay: time.Millisecond * 30,
		// Maximum delay duration
		MaxDelay: time.Second,
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

func consumerChain() {

	for {
		msg := <-chMessages
		sendTelegramMessage(telegramBot, parameters.TelegramChatId, messageFormat(msg, true))

		if twitterBot != nil {
			sendTwitterPost(twitterBot, messageFormat(msg, false))
		}
		//telegramMessageFormat(<-chMessages)
	}
}

func consumerCex() {

	for {
		msg := <-chMessagesCex

		sendTelegramMessage(telegramBot, parameters.TelegramChatId, formatCexMessage(msg))

		if twitterBot != nil {
			sendTwitterPost(twitterBot, formatCexMessage(msg))
		}
		//formatCexMessage(<-chMessagesCex)
	}
}
