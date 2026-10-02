package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/joho/godotenv"
	"github.com/michimani/gotwi"
	"github.com/michimani/gotwi/tweet/managetweet"
	"github.com/michimani/gotwi/tweet/managetweet/types"
	"github.com/mymmrac/telego"
)

const baseAlph = 1e18

var coinGeckoPrice float64
var priceMu sync.RWMutex

func alphUSDPrice() float64 {
	priceMu.RLock()
	defer priceMu.RUnlock()
	return coinGeckoPrice
}

type KnownWallet struct {
	Address      string `json:"address"`
	ExchangeName string `json:"exchangeName"`
	Name         string `json:"name"`
	State        string `json:"state"`
	Type         string `json:"type"`
}

type CoinGeckoPrice struct {
	Alephium struct {
		Usd float64 `json:"usd"`
	} `json:"alephium"`
}

type CsvArticles struct {
	Title string `csv:"title"`
	Url   string `csv:"url"`
}

type Amount struct {
	Value  float64
	Symbol string
}

func (a Amount) formatHuman() string {
	amount := float64(a.Value)

	switch {
	case amount < 0:
		return fmt.Sprintf("%.3f %s", amount, a.Symbol)
	case math.Round(amount) >= 1000.0 && math.Round(amount) < 1e6:
		return fmt.Sprintf("%.2fK %s", amount/1000.0, a.Symbol)
	case math.Round(amount) >= float64(1e6):
		return fmt.Sprintf("%.2fM %s", amount/float64(1e6), a.Symbol)
	default:
		return fmt.Sprintf("%.2f %s", amount, a.Symbol)
	}
}

var trackTokens map[string]float64
var metadataMu sync.RWMutex
var tokensByID map[string]Token

// A shared client reuses connections across polling cycles and workers.
var httpRetryClient = func() *retryablehttp.Client {
	client := retryablehttp.NewClient()
	client.RetryMax = 3
	client.HTTPClient.Timeout = 30 * time.Second
	client.Logger = nil
	return client
}()

func loadEnv() {
	err := godotenv.Load(".env")
	if err != nil {
		log.Printf("Will load from env var\n")
	}

	chatIdInt, err := strconv.ParseInt(os.Getenv("CHAT_ID"), 10, 64)
	if err != nil {
		log.Fatalf("error getting chat id from env, err: %s\n", err)
	}
	parameters.TelegramChatId = chatIdInt
	parameters.TelegramTokenApi = os.Getenv("TELEGRAM_TOKEN")

	parameters.TwitterAccessToken = os.Getenv("TWITTER_ACCESS_TOKEN")
	parameters.TwitterAccessTokenSecret = os.Getenv("TWITTER_ACCESS_SECRET")

	parameters.FullnodeApi = os.Getenv("FULLNODE_API_BASE")
	parameters.WsFullnode = os.Getenv("FULLNODE_WS_BASE")

	parameters.ExplorerApi = os.Getenv("API_EXPLORER_BASE")
	parameters.FrontendExplorerUrl = os.Getenv("FRONTEND_EXPLORER")
	parameters.KnownWalletUrl = os.Getenv("KNOWN_WALLETS_URL")
	parameters.PriceUrl = os.Getenv("PRICE_URL")
	parameters.TokenListUrl = os.Getenv("TOKEN_LIST_URL")

	debugEnv := os.Getenv("DEBUG")
	parameters.debugMode = false
	if debugEnv != "" {
		debugMode, err := strconv.ParseBool(debugEnv)
		if err != nil {
			log.Printf("debug mode cannot be parsed, %s", err)
			parameters.debugMode = false
		}
		parameters.debugMode = debugMode
	}

	minAmountTriggerFloat, err := strconv.ParseFloat(os.Getenv("MIN_AMOUNT_TRIGGER"), 64)
	if err != nil {
		log.Printf("error getting min amount trigger from env, err: %s", err)
		minAmountTriggerFloat = 5000
	}

	parameters.MinAmountTrigger = minAmountTriggerFloat

	MinAmountCexTriggerUsdFloat, err := strconv.ParseFloat(os.Getenv("MIN_AMOUNT_TRIGGER_CEX_USD"), 64)
	if err != nil {
		log.Printf("error getting min amount trigger cex from env, err: %s", err)
		MinAmountCexTriggerUsdFloat = 5000
	}

	parameters.MinAmountCexTriggerUsd = MinAmountCexTriggerUsdFloat

	parameters.MinAmountDexTriggerUsd = 5000
	if value := os.Getenv("MIN_AMOUNT_TRIGGER_DEX_USD"); value != "" {
		threshold, err := strconv.ParseFloat(value, 64)
		if err != nil || threshold < 0 || math.IsNaN(threshold) || math.IsInf(threshold, 0) {
			log.Printf("invalid MIN_AMOUNT_TRIGGER_DEX_USD %q; using 5000", value)
		} else {
			parameters.MinAmountDexTriggerUsd = threshold
		}
	}

	pollingIntervalSecInt, err := strconv.ParseInt(os.Getenv("POLLING_INTERVAL_SEC"), 10, 64)
	if err != nil || pollingIntervalSecInt <= 0 {
		log.Printf("invalid POLLING_INTERVAL_SEC %q (%v); using 120s", os.Getenv("POLLING_INTERVAL_SEC"), err)
		pollingIntervalSecInt = 120
	}
	parameters.PollingIntervalSec = pollingIntervalSecInt

}

func getHttp(url string) ([]byte, int, error) {
	resp, err := httpRetryClient.Get(url)

	if err != nil {
		log.Printf("HTTP query error after %d retries: %s, url: %s\n", httpRetryClient.RetryMax, err, url)
		return []byte{}, 0, fmt.Errorf("HTTP query error: %s, url: %s\n", err, url)
	}

	defer resp.Body.Close()

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, fmt.Errorf("read response from %s: %w", url, err)
	}
	statusCode := resp.StatusCode

	if statusCode != 200 {
		status := resp.Status
		return []byte{}, statusCode, fmt.Errorf("error code with url: %s, %d, %s", url, statusCode, status)
	}

	//bodyString := string(bodyBytes)
	//fmt.Println("API Response as String:\n" + bodyString)

	return bodyBytes, statusCode, nil
}

func randInt(min int, max int) int {
	return min + rand.Intn(max-min)
}

func getRndArticles() CsvArticles {

	f, err := os.Open("./articles.csv")
	if err != nil {
		log.Fatal(err)
	}

	defer f.Close()

	csvReader := csv.NewReader(f)
	csvReader.Comma = ','
	data, err := csvReader.ReadAll()
	if err != nil {
		log.Fatal(err)
	}
	rndArticleIndex := randInt(1, len(data))
	article := CsvArticles{Title: data[rndArticleIndex][0], Url: data[rndArticleIndex][1]}

	return article
}

func messageFormat(msg Message, isTelegram bool) string {

	amountChain := msg.amountChain
	namedWalletFrom := getAddressName(&msg.from)
	namedWalletTo := getAddressName(&msg.to)

	var amountFiatString string

	symbol := msg.tokenData.Symbol
	if msg.tokenData.Name == "" {
		symbol = "ALPH"
		amountFiat := Amount{Value: amountChain * alphUSDPrice(), Symbol: "USDT"}
		amountFiatString = "(" + amountFiat.formatHuman() + ")"
	}

	if symbol != "ALPH" {
		decimal := float64(msg.tokenData.Decimals)
		amountChain = msg.amountChain / math.Pow(10.0, decimal)
	}

	humanFormatAmount := Amount{Value: amountChain, Symbol: "$" + symbol}.formatHuman()

	addrFrom, alertEmojiFrom := formatAddress(&namedWalletFrom, msg.from, amountChain, false)
	addrTo, alertEmojiTo := formatAddress(&namedWalletTo, msg.to, amountChain, true)

	var alertEmoji string
	if alertEmojiFrom != "" {
		alertEmoji = alertEmojiFrom
	}

	if alertEmojiTo != "" {
		alertEmoji = alertEmojiTo
	}

	var text string
	if isTelegram {

		groupsString := fmt.Sprintf("(%d -> %d)", msg.groupFrom, msg.groupTo)

		text = fmt.Sprintf("%s %s transferred %s\n%s to %s %s\n\n<a href='%s/#/transactions/%s'>TX link</a>\n", alertEmoji, humanFormatAmount, groupsString, addrFrom, addrTo, amountFiatString, parameters.FrontendExplorerUrl, msg.txId)

		rndArticle := getRndArticles()
		text += fmt.Sprintf("Featured article: <a href='%s'>%s</a>", rndArticle.Url, rndArticle.Title)

	} else {
		text = fmt.Sprintf("%s %s transferred\n%s to %s %s\n\n%s/#/transactions/%s\n", alertEmoji, humanFormatAmount, addrFrom, addrTo, amountFiatString, parameters.FrontendExplorerUrl, msg.txId)

		//rndArticle := getRndArticles()
		//text += fmt.Sprintf("Feat. article: %s", rndArticle.Url)
		//text += "\n\n#alephium"

	}

	fmt.Println(text)
	if !isTelegram && len(text) > 280 {
		return text[0:280]
	}

	return text

}

func formatAddress(knownWallet *KnownWallet, address string, amount float64, to bool) (string, string) {

	truncated := fmt.Sprintf("%s...%s", address[0:3], address[len(address)-3:])

	// direclty return if address is not known
	if *knownWallet == (KnownWallet{}) {
		return truncated, ""
	}

	if knownWallet.Name != "" {
		truncated = knownWallet.Name
	}

	var alertEmoji string
	if knownWallet.ExchangeName != "" {
		alertEmoji = strings.Repeat("🟢 ", len(strconv.Itoa(int(amount)))-1)
		if to {
			alertEmoji = strings.Repeat("💸 ", len(strconv.Itoa(int(amount)))-1)
		}
	}

	return truncated, alertEmoji

}

func formatCexMessage(msg MessageCex) string {
	if msg.ExchangeName == "Powfi" {
		action, emoji := "Sell", "🔴"
		if strings.EqualFold(msg.Side, "buy") {
			action, emoji = "Buy", "🟢"
		}
		valueLabel := "Total"
		if msg.AmountFiat.Symbol == "USD" {
			valueLabel = "Estimated value"
		}
		return fmt.Sprintf("%s Swap: #Powfi\n\n%s ALPH\nVolume: %s\nSwapped against: %s\n%s: %s\nRate: %.8g %s per ALPH\n\nhttps://powfi.alephium.org/swap/\n%s/#/transactions/%s", emoji, action, msg.AmountLeft.formatHuman(), msg.QuoteAmount.formatHuman(), valueLabel, msg.AmountFiat.formatHuman(), msg.Price, msg.QuoteAmount.Symbol, parameters.FrontendExplorerUrl, msg.TxID)
	}

	var sideAction string
	var sideActionEmoji string
	if strings.ToLower(msg.Side) == "buy" {
		sideAction = "Buy"
		sideActionEmoji = "🟢"
	}

	if strings.ToLower(msg.Side) == "sell" {
		sideAction = "Sell"
		sideActionEmoji = ""
	}

	text := fmt.Sprintf("%s Exchange: #%s\n\n%s Volume: %s \nTotal: %s (at %.3f USDT)\n\n#exchange", sideActionEmoji, msg.ExchangeName, sideAction, msg.AmountLeft.formatHuman(), msg.AmountFiat.formatHuman(), msg.Price)

	if msg.TxID != "" {
		text += fmt.Sprintf("\n\n%s/#/transactions/%s", parameters.FrontendExplorerUrl, msg.TxID)
	}
	fmt.Println(text)
	return text

}

func getAddressName(address *string) KnownWallet {
	metadataMu.RLock()
	defer metadataMu.RUnlock()

	if knownAddress, ok := KnownWallets[*address]; ok {
		fmt.Println(knownAddress)
		return knownAddress
	}

	return KnownWallet{}
}

func sendTelegramMessage(b *telego.Bot, chatId int64, message string) {
	chatID := telego.ChatID{ID: chatId}
	_, err := b.SendMessage(&telego.SendMessageParams{
		ChatID:             chatID,
		Text:               message,
		LinkPreviewOptions: &telego.LinkPreviewOptions{IsDisabled: true},
		ParseMode:          telego.ModeHTML,
	})
	if err != nil {
		log.Printf("error telegram bot: %s", err)
	}
}

func sendTwitterPost(c *gotwi.Client, text string) (string, error) {
	p := &types.CreateInput{
		Text: gotwi.String(text),
		//ReplySettings: gotwi.String("everyone"),
	}

	res, err := managetweet.Create(context.Background(), c, p)
	if err != nil {
		log.Printf("Error sending tweet, err: %s", err)
		return "", err
	}

	return gotwi.StringValue(res.Data.ID), nil
}

func updatePrice() {
	dataBytes, _, err := getHttp(parameters.PriceUrl)
	if err != nil {
		log.Printf("Error getting ALPH/USD price: %v", err)
		return
	}
	var price CoinGeckoPrice
	if err := json.Unmarshal(dataBytes, &price); err != nil {
		log.Printf("Error decoding ALPH/USD price: %v", err)
		return
	}
	value := price.Alephium.Usd
	if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
		log.Printf("Invalid ALPH/USD price: %g; retaining previous price", value)
		return
	}
	priceMu.Lock()
	coinGeckoPrice = value
	priceMu.Unlock()
}

func updateKnownWallet() {
	data, _, err := getHttp(parameters.KnownWalletUrl)
	if err != nil {
		log.Printf("Error refreshing known wallets: %v", err)
		return
	}
	var wallets map[string]KnownWallet
	if err := json.Unmarshal(data, &wallets); err != nil {
		log.Printf("Error decoding known wallets: %v", err)
		return
	}
	metadataMu.Lock()
	KnownWallets = wallets
	metadataMu.Unlock()
}

func updateTokens() {
	data, _, err := getHttp(parameters.TokenListUrl)
	if err != nil {
		log.Printf("Error refreshing tokens: %v", err)
		return
	}
	var tokens TokenList
	if err := json.Unmarshal(data, &tokens); err != nil {
		log.Printf("Error decoding tokens: %v", err)
		return
	}
	index := make(map[string]Token, len(tokens.Tokens))
	for _, token := range tokens.Tokens {
		if _, exists := index[token.ID]; !exists {
			index[token.ID] = token
		}
	}
	metadataMu.Lock()
	Tokens = tokens
	tokensByID = index
	metadataMu.Unlock()
}

func searchTokenData(contractId string) Token {
	metadataMu.RLock()
	defer metadataMu.RUnlock()
	return tokensByID[contractId]
}

func loadTokensToTrack() {

	trackTokens = make(map[string]float64)
	tokensEnv := strings.Split(os.Getenv("TOKENS"), ",")

	// load token amount trigger
	for _, token := range tokensEnv {

		tokenId := strings.Split(token, ";")[0]
		amountTrigger, err := strconv.ParseFloat(strings.Split(token, ";")[1], 64)
		if err != nil {
			fmt.Printf("cannot get token trigger amount, %s, err: %s\n", tokenId, err)

		}

		trackTokens[tokenId] = amountTrigger
	}
}

func testsAlert(chTxs chan Tx) {

	//testWallet := []KnownWallet{{Address: "1iAFqJZm6PMTUDquiV7MtDse6oHBxRcdsq2N3qzsSZ9Q", Name: "test"}}
	//KnownWallets = append(KnownWallets, testWallet...)

	chTxs <- Tx{id: "c4c7f56e6b4ddebd2d81e93031f7fb82680885599fc87ce3ea7d2938b55b6c54", height: math.MaxInt - 100}

	// ayin test
	chTxs <- Tx{id: "895716a20912805c2029c61b1d78e2e2eeb78c49e9b26f4207257214c59ef408", height: math.MaxInt - 100}

	// usdt test
	chTxs <- Tx{id: "19ad56db69577087013ecbdaf6ebbd0a3246e7a539c3b32243c85ab09d0e1fd5", height: math.MaxInt - 100}

	//wbtc test
	chTxs <- Tx{id: "90cff504fe44e175817d26f95b48732745a9559ad37659c277780f1941ed2540", height: math.MaxInt - 100}

	//apad test
	chTxs <- Tx{id: "9e5a59317a2966911bc2dd62a985d0c4f497617df408c64d2e5ab273c28a0988", height: math.MaxInt - 100}

	//multiple transfer test
	chTxs <- Tx{id: "2720df2fc2d83b1b9f7891f9fa852e96806d93132a6fdfe76a6791b7a88d0425", height: math.MaxInt - 100}

	// abx mexc
	chTxs <- Tx{id: "905bf9f6d6dbcf518c948f97c634203cbc9e9b1e57820a79bb876370dcbaab88", height: math.MaxInt - 100}

	// testing humamize millio
	chTxs <- Tx{id: "904d3e2c2b49f70b86113e23ed46d973afeef8985ffa1e263fc90f868b65dc84", height: math.MaxInt - 100}

	// testing tx between ignored address pairs
	chTxs <- Tx{id: "7cb68e9f593be37c40e4b3d156c007d2d146c86eac922df961850426891d7f08", height: math.MaxInt - 100}

}
