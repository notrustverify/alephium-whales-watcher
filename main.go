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
	from        string
	to          string
	amountChain float64
	txId        string
	tokenData   Token
}

type MessageCex struct {
	Side         string
	AmountLeft   Amount
	AmountFiat   Amount
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
	cronScheduler.Every("1h").Do(updateTokens)
	cronScheduler.StartAsync()
	rand.NewSource(time.Now().UnixNano())

	chMessages := make(chan Message, 100)
	chMessagesCex := make(chan MessageCex, 100)
	chTxs := make(chan string, 100)

	telegramBot = initTelegram()
	var err error
	twitterBot, err = initTwitter()
	if err != nil {
		log.Printf("cannot init twitter, err: %s\n", err)
		twitterBot = nil
	}

	for w := 1; w <= 10; w++ {
		go checkTx(chTxs, chMessages, w)
		go messageConsumer(chMessagesCex, chMessages)
	}

	if parameters.debugMode {
		testsAlert(chTxs)
	}

	go getCexTrades(chMessagesCex)
	getChain(apiClient, ctxAlephium, chTxs)
}

func checkTx(ch chan string, msgCh chan Message, wId int) {
	for {

		select {
		case tx := <-ch:
			getTxData(tx, msgCh, wId)

		default:
			time.Sleep(500 * time.Millisecond)
		}
	}
}

func getChain(apiClient *openapiclient.APIClient, ctxAlephium context.Context, chTxs chan string) {
	for {
		timeNow := time.Now().Unix()
		getBlocksFullnode(apiClient, &ctxAlephium, timeNow*1000-parameters.PollingIntervalSec*1000, timeNow*1000, chTxs)

		log.Println("Sleepy sleepy")
		time.Sleep(time.Duration(parameters.PollingIntervalSec) * time.Second)
	}
}

func getCexTrades(msgCh chan MessageCex) {
	for {
		t := time.Now().Unix()

		go getTradesGate(t-parameters.PollingIntervalSec, t, msgCh)
		go getTradesMexc(t*1000-parameters.PollingIntervalSec*1000, t*1000, msgCh)
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
		case msg := <-chMessages:
			sendTelegramMessage(telegramBot, parameters.TelegramChatId, messageFormat(msg, true))

			if twitterBot != nil {
				sendTwitterPost(twitterBot, messageFormat(msg, false))
			}
		//telegramMessageFormat(<-chMessages)
		default:
			time.Sleep(500 * time.Millisecond)
		}

	}
}
