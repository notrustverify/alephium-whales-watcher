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
	from      string
	to        string
	amount    float64
	txId      string
	tokenData Token
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
	MinAmountAyinTrigger     float64
	MinAmountUsdtTrigger     float64
	MinAmountEthTrigger      float64
	MinAmountBtcTrigger      float64
	PollingIntervalSec       int64
	KnownWalletUrl           string
	PriceUrl                 string
	TokenListUrl             string
}

var telegramBot *telego.Bot
var twitterBot *gotwi.Client
var KnownWallets []KnownWallet
var Tokens TokenList

var parameters Parameters

func main() {

	loadEnv()
	loadTokensToTrack()

	getRndArticles()
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

	//testWallet := []KnownWallet{{Address: "1iAFqJZm6PMTUDquiV7MtDse6oHBxRcdsq2N3qzsSZ9Q", Name: "test"}}
	//KnownWallets = append(KnownWallets, testWallet...)

	//ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	//telegramBot := initTelegram()
	//defer cancel()
	//telegramBot.Start(ctx)

	telegramBot = initTelegram()
	var err error
	twitterBot, err = initTwitter()
	if err != nil {
		log.Printf("cannot init twitter, err: %s\n", err)
		twitterBot = nil
	}

	//log.Printf("%+v\n", knownWallets)

	chTxs <- "c4c7f56e6b4ddebd2d81e93031f7fb82680885599fc87ce3ea7d2938b55b6c54"

	// ayin test
	chTxs <- "895716a20912805c2029c61b1d78e2e2eeb78c49e9b26f4207257214c59ef408"

	// usdt test
	//chTxs <- "19ad56db69577087013ecbdaf6ebbd0a3246e7a539c3b32243c85ab09d0e1fd5"
	//wbtc test
	//chTxs <- "90cff504fe44e175817d26f95b48732745a9559ad37659c277780f1941ed2540"

	//getTxData(apiClient, &ctxAlephium, "d317add70567414626b6d7e5fd26e841cf5d81de6e2adb8e1a6d6968f47848ba")
	for w := 1; w <= 10; w++ {
		go checkTx(chTxs, chMessages, w)
		go messageConsumer(chMessagesCex, chMessages)
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
		t := time.Now().Unix()
		getBlocksFullnode(apiClient, &ctxAlephium, t*1000-parameters.PollingIntervalSec*1000, t*1000, chTxs)

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
