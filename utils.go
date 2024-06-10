package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

const BASE_ALPH = 1e18

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

	minAmountTriggerFloat, err := strconv.ParseFloat(os.Getenv("MIN_AMOUNT_TRIGGER"), 64)
	if err != nil {
		log.Printf("error getting min amount trigger from env, err: %s", err)
		minAmountTriggerFloat = 5000
	}
	parameters.MinAmountTrigger = minAmountTriggerFloat

	pollingIntervalSecInt, err := strconv.ParseInt(os.Getenv("POLLING_INTERVAL_SEC"), 10, 64)
	if err != nil {
		log.Printf("error getting polling interval from env, err: %s\n", err)
		parameters.PollingIntervalSec = 120
	}
	parameters.PollingIntervalSec = pollingIntervalSecInt

}

func getHttp(url string) ([]byte, int, error) {
	retryClient := retryablehttp.NewClient()
	retryClient.RetryMax = 10
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
	csvReader.Comma = ';'
	data, err := csvReader.ReadAll()
	if err != nil {
		log.Fatal(err)
	}
	rndArticleIndex := randInt(1, len(data))
	article := CsvArticles{Title: data[rndArticleIndex][0], Url: data[rndArticleIndex][1]}

	return article
}

func messageFormat(msg Message, isTelegram bool) string {

	humanFormatAmount := fmt.Sprintf("%.2f ALPH", msg.amount)
	namedWalletFrom := getAddressName(&msg.from)
	namedWalletTo := getAddressName(&msg.to)

	if msg.amount >= 1000 {
		humanFormatAmount = fmt.Sprintf("%.2f K ALPH", msg.amount/1000.0)

	} else if msg.amount >= 1e6 {
		humanFormatAmount = fmt.Sprintf("%.2f M ALPH", msg.amount/float64(1e6))
	}

	amountFiat := msg.amount * coinGeckoPrice
	amountFiatString := fmt.Sprintf("%.2f USDT", amountFiat)
	if amountFiat >= 1000 {
		amountFiatString = fmt.Sprintf("%.2f K USDT", amountFiat/1000.0)
	} else if amountFiat >= 1e6 {
		amountFiatString = fmt.Sprintf("%.2f M USDT", amountFiat/float64(1e6))
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
		text = fmt.Sprintf("%s %s transferred\n%s to %s (%s)\n\n<a href='%s/#/transactions/%s'>TX link</a>\n", alertEmoji, humanFormatAmount, addrFrom, addrTo, amountFiatString, parameters.FrontendExplorerUrl, msg.txId)

		rndArticle := getRndArticles()
		text += fmt.Sprintf("Featured article: <a href='%s'>%s</a>", rndArticle.Url, rndArticle.Title)

	} else {
		text = fmt.Sprintf("%s %s transferred\n%s to %s (%s)\n\n%s/#/transactions/%s\n", alertEmoji, humanFormatAmount, addrFrom, addrTo, amountFiatString, parameters.FrontendExplorerUrl, msg.txId)

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
	if amount >= 1000 {
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

func removeElement(a []string, indexToRemove int) []string {
	a[indexToRemove] = a[len(a)-1]
	return a[:len(a)-1]
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
