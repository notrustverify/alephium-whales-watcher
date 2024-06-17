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
	"slices"
	"strconv"
	"strings"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/joho/godotenv"
	"github.com/michimani/gotwi"
	"github.com/michimani/gotwi/tweet/managetweet"
	"github.com/michimani/gotwi/tweet/managetweet/types"
	"github.com/mymmrac/telego"
)

const baseAlph = 1e18

var coinGeckoPrice float64

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

var trackTokens map[string]float64

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
	parameters.ExplorerApi = os.Getenv("API_EXPLORER_BASE")
	parameters.FrontendExplorerUrl = os.Getenv("FRONTEND_EXPLORER")
	parameters.KnownWalletUrl = os.Getenv("KNOWN_WALLETS_URL")
	parameters.PriceUrl = os.Getenv("PRICE_URL")
	parameters.TokenListUrl = os.Getenv("TOKEN_LIST_URL")

	minAmountTriggerFloat, err := strconv.ParseFloat(os.Getenv("MIN_AMOUNT_TRIGGER"), 64)
	if err != nil {
		log.Printf("error getting min amount trigger from env, err: %s", err)
		minAmountTriggerFloat = 5000
	}
	minAmountTriggerAyinFloat, err := strconv.ParseFloat(os.Getenv("MIN_AMOUNT_AYIN_TRIGGER"), 64)
	if err != nil {
		log.Printf("error getting min amount trigger from env, err: %s", err)
		minAmountTriggerAyinFloat = 500
	}

	minAmountTriggerUsdtFloat, err := strconv.ParseFloat(os.Getenv("MIN_AMOUNT_USDT_TRIGGER"), 64)
	if err != nil {
		log.Printf("error getting min amount trigger from env, err: %s", err)
		minAmountTriggerUsdtFloat = 5000
	}

	minAmountTriggerEthFloat, err := strconv.ParseFloat(os.Getenv("MIN_AMOUNT_USDT_TRIGGER"), 64)
	if err != nil {
		log.Printf("error getting min amount trigger from env, err: %s", err)
		minAmountTriggerEthFloat = 1
	}

	minAmountTriggerBtcFloat, err := strconv.ParseFloat(os.Getenv("MIN_AMOUNT_USDT_TRIGGER"), 64)
	if err != nil {
		log.Printf("error getting min amount trigger from env, err: %s", err)
		minAmountTriggerBtcFloat = 0.5
	}

	parameters.MinAmountTrigger = minAmountTriggerFloat
	parameters.MinAmountAyinTrigger = minAmountTriggerAyinFloat
	parameters.MinAmountUsdtTrigger = minAmountTriggerUsdtFloat
	parameters.MinAmountEthTrigger = minAmountTriggerEthFloat
	parameters.MinAmountBtcTrigger = minAmountTriggerBtcFloat

	pollingIntervalSecInt, err := strconv.ParseInt(os.Getenv("POLLING_INTERVAL_SEC"), 10, 64)
	if err != nil {
		log.Printf("error getting polling interval from env, err: %s\n", err)
		parameters.PollingIntervalSec = 120
	}
	parameters.PollingIntervalSec = pollingIntervalSecInt

}

func getHttp(url string) ([]byte, int, error) {
	retryClient := retryablehttp.NewClient()
	retryClient.RetryMax = 3
	retryClient.Logger = nil
	resp, err := retryClient.Get(url)

	if err != nil {
		log.Fatalf("HTTP query error: %s, url: %s\n", err, url)
		return []byte{}, 0, fmt.Errorf("HTTP query error: %s, url: %s\n", err, url)
	}

	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(resp.Body)
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

	amountChain := msg.amount
	namedWalletFrom := getAddressName(&msg.from)
	namedWalletTo := getAddressName(&msg.to)

	symbol := msg.tokenData.Symbol
	if msg.tokenData.Name == "" {
		symbol = "ALPH"
	}

	if symbol != "ALPH" {
		decimal := float64(msg.tokenData.Decimals)
		amountChain = msg.amount / math.Pow(10.0, decimal)
	}

	humanFormatAmount := fmt.Sprintf("%.2f", amountChain)
	if amountChain < 1 {
		humanFormatAmount = fmt.Sprintf("%.3f", amountChain)
	} else if math.Round(amountChain) >= 1000.0 {
		humanFormatAmount = fmt.Sprintf("%.2f K", amountChain/1000.0)
	} else if math.Round(amountChain) >= 1e6 {
		humanFormatAmount = fmt.Sprintf("%.2f M", amountChain/float64(1e6))
	}

	var amountFiatString string
	if symbol == "ALPH" {
		amountFiat := amountChain * coinGeckoPrice
		amountFiatString = fmt.Sprintf("(%.2f USDT)", amountFiat)
		if math.Round(amountFiat) >= 1000.0 {
			amountFiatString = fmt.Sprintf("(%.2f K USDT)", amountFiat/1000.0)
		} else if math.Round(amountFiat) >= 1e6 {
			amountFiatString = fmt.Sprintf("(%.2f M USDT)", amountFiat/float64(1e6))
		}
	}
	addrFrom, alertEmojiFrom := formatAddress(&namedWalletFrom, msg.from, msg.amount, false)
	addrTo, alertEmojiTo := formatAddress(&namedWalletTo, msg.to, msg.amount, true)

	var alertEmoji string
	if alertEmojiFrom != "" {
		alertEmoji = alertEmojiFrom
	}

	if alertEmojiTo != "" {
		alertEmoji = alertEmojiTo
	}

	var text string
	if isTelegram {
		text = fmt.Sprintf("%s %s $%s transferred\n%s to %s %s\n\n<a href='%s/#/transactions/%s'>TX link</a>\n", alertEmoji, humanFormatAmount, symbol, addrFrom, addrTo, amountFiatString, parameters.FrontendExplorerUrl, msg.txId)

		rndArticle := getRndArticles()
		text += fmt.Sprintf("Featured article: <a href='%s'>%s</a>", rndArticle.Url, rndArticle.Title)

	} else {
		text = fmt.Sprintf("%s %s %s transferred\n%s to %s %s\n\n%s/#/transactions/%s\n", alertEmoji, humanFormatAmount, symbol, addrFrom, addrTo, amountFiatString, parameters.FrontendExplorerUrl, msg.txId)

		rndArticle := getRndArticles()
		text += fmt.Sprintf("Feat. article: %s", rndArticle.Url)
		text += "\n\n#alephium"

	}

	fmt.Println(text)
	if !isTelegram && len(text) > 280 {
		return text[0:280]
	}

	return text

}

func messageFormatContract(msg Message, isTelegram bool) string {
	amountChain := msg.amount
	namedWalletFrom := getAddressName(&msg.from)
	namedWalletTo := getAddressName(&msg.to)

	symbol := msg.tokenData.Symbol
	if msg.tokenData.Name == "" {
		symbol = "ALPH"
	}

	if symbol != "ALPH" {
		decimal := float64(msg.tokenData.Decimals)
		amountChain = msg.amount / math.Pow(10.0, decimal)
	}

	humanFormatAmount := humanizeAmount(amountChain, msg.tokenData.Symbol)
	amountFiatString := ""
	if symbol == "ALPH" {
		amountFiatString = humanizeAmount(amountChain*coinGeckoPrice, msg.tokenData.Symbol)
	}

	addrFrom, _ := formatAddress(&namedWalletFrom, msg.from, msg.amount, false)
	addrTo, _ := formatAddress(&namedWalletTo, msg.to, msg.amount, true)

	text := ""
	if isTelegram {
		text = fmt.Sprintf("📜Contract %s $%s transferred\n%s to %s %s\n\n<a href='%s/#/transactions/%s'>TX link</a>\n", humanFormatAmount, symbol, addrFrom, addrTo, amountFiatString, parameters.FrontendExplorerUrl, msg.txId)

		rndArticle := getRndArticles()
		text += fmt.Sprintf("Featured article: <a href='%s'>%s</a>", rndArticle.Url, rndArticle.Title)

	} else {
		text = fmt.Sprintf("📜 %s %s transferred\n%s to %s %s\n\n%s/#/transactions/%s\n", humanFormatAmount, symbol, addrFrom, addrTo, amountFiatString, parameters.FrontendExplorerUrl, msg.txId)

		rndArticle := getRndArticles()
		text += fmt.Sprintf("Feat. article: %s", rndArticle.Url)
		text += "\n\n#alephium"

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
			alertEmoji = strings.Repeat("🔴 ", len(strconv.Itoa(int(amount)))-1)
		}
	}

	return truncated, alertEmoji

}

func formatCexMessage(msg MessageCex) string {

	var sideAction string
	var sideActionEmoji string
	if strings.ToLower(msg.Side) == "buy" {
		sideAction = "Buy"
		sideActionEmoji = "🟢"
	}

	if strings.ToLower(msg.Side) == "sell" {
		sideAction = "Sell"
		sideActionEmoji = "🔴"
	}

	text := fmt.Sprintf("%s Exchange: #%s\n\n%s Volume: %s \nTotal: %s (at %.3f USDT)\n\n#exchange", sideActionEmoji, msg.ExchangeName, sideAction, humanizeAmount(msg.Amount, "ALPH"), humanizeAmount(msg.AmountFiat, "USDT"), msg.Price)

	fmt.Println(text)
	return text

}

func humanizeAmount(amount float64, symbol string) string {
	amountStr := fmt.Sprintf("%.2f %s", amount, symbol)
	if amount < 1 {
		amountStr = fmt.Sprintf("%.3f %s", amount, symbol)
	} else if amount >= 1000.0 {
		amountStr = fmt.Sprintf("%.2f K %s", amount/1000.0, symbol)
	} else if amount >= 1e6 {
		amountStr = fmt.Sprintf("%.2f M %s", amount/float64(1e6), symbol)
	}

	return amountStr

}

func getAddressName(address *string) KnownWallet {

	idx := slices.IndexFunc(KnownWallets, func(c KnownWallet) bool { return c.Address == *address })

	if idx >= 0 {
		return KnownWallets[idx]
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
		Text:          gotwi.String(text),
		ReplySettings: gotwi.String("following"),
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
		log.Printf("Error getting price\n%s\n", err)
	}

	if len(dataBytes) > 0 {
		var coinGeckoApi CoinGeckoPrice
		json.Unmarshal(dataBytes, &coinGeckoApi)
		coinGeckoPrice = coinGeckoApi.Alephium.Usd
	}

}

func updateKnownWallet() {
	dataBytes, _, err := getHttp(parameters.KnownWalletUrl)
	if err != nil {
		log.Printf("Error getting know wallet\n%s\n", err)
	}

	json.Unmarshal(dataBytes, &KnownWallets)

}

func updateTokens() {
	dataBytes, _, err := getHttp(parameters.TokenListUrl)
	if err != nil {
		log.Printf("Error getting know wallet\n%s\n", err)
	}

	json.Unmarshal(dataBytes, &Tokens)

}

func searchTokenData(contractId string) Token {
	for _, item := range Tokens.Tokens {
		if item.ID == contractId {
			return item
		}

	}
	return Token{}
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
