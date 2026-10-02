package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math"
	"math/big"
	"os"
	"strconv"
	"strings"
	"time"
)

const powfiUSDT = "556d9582463fe44fbd108aedc9f409f69086dc78d994b88ea6c9e65f8bf98e00"
const powfiALPH = "0000000000000000000000000000000000000000000000000000000000000000"

type powfiField struct {
	Type  string `json:"type"`
	Value string `json:"value"`
}
type powfiEvent struct {
	ContractAddress string       `json:"contractAddress"`
	TxID            string       `json:"txId"`
	EventIndex      int          `json:"eventIndex"`
	Fields          []powfiField `json:"fields"`
}
type powfiPage struct {
	Events    []powfiEvent `json:"events"`
	NextStart int          `json:"nextStart"`
}

// Each configured pool has its own cursor. Starting at the current count avoids
// replaying historical alerts; cursors stay in memory for the process lifetime.
func startPowfiWatchers(ch chan MessageCex) {
	raw := strings.TrimSpace(os.Getenv("POWFI_POOL_ADDRESSES"))
	if raw == "" {
		return
	}
	interval := 15 * time.Second
	if value := os.Getenv("POWFI_POLL_INTERVAL_SEC"); value != "" {
		seconds, err := strconv.Atoi(value)
		if err != nil || seconds <= 0 {
			log.Printf("Powfi: invalid polling interval %q; using 15s", value)
		} else {
			interval = time.Duration(seconds) * time.Second
		}
	}
	seen := map[string]bool{}
	for _, address := range strings.Split(raw, ",") {
		address = strings.TrimSpace(address)
		if address == "" || seen[address] {
			continue
		}
		seen[address] = true
		go watchPowfiPool(address, interval, ch)
	}
}

func powfiGet(path string, target interface{}) error {
	base := fullnodeBaseURL(parameters.FullnodeApi, false)
	body, _, err := getHttp(base + path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(body, target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func watchPowfiPool(address string, interval time.Duration, ch chan MessageCex) {
	path := "/events/contract/" + address
	cursor, alphIndex := 0, 0
	quoteToken := Token{}
	initialized := false
	for {
		err := func() error {
			if !initialized {
				var state struct {
					CodeHash  string       `json:"codeHash"`
					ImmFields []powfiField `json:"immFields"`
				}
				if err := powfiGet("/contracts/"+address+"/state", &state); err != nil {
					return err
				}
				if state.CodeHash != "1642bd2f249a1a189e4448ecaa4e048a85e9fbf9f7dbf4fe176dbc56c93e8d7c" {
					return fmt.Errorf("unsupported pool code hash %s; expected Powfi CLMM Pool v1", state.CodeHash)
				}
				if len(state.ImmFields) < 8 {
					return fmt.Errorf("unsupported pool state")
				}
				token0, token1 := state.ImmFields[6].Value, state.ImmFields[7].Value
				switch {
				case token0 == powfiALPH && token1 != powfiALPH:
					alphIndex = 0
				case token1 == powfiALPH && token0 != powfiALPH:
					alphIndex = 1
				default:
					return fmt.Errorf("unsupported pool tokens: %s / %s; expected Powfi CLMM pool containing ALPH", token0, token1)
				}
				quoteID := token1
				if alphIndex == 1 {
					quoteID = token0
				}
				quoteToken = searchTokenData(quoteID)
				if quoteID == powfiUSDT {
					quoteToken = Token{ID: quoteID, Symbol: "USDT", Decimals: 6}
				}
				if quoteToken.Symbol == "" || quoteToken.Decimals < 0 || quoteToken.Decimals > 255 {
					return fmt.Errorf("missing token metadata for %s; add it to TOKEN_LIST_URL", quoteID)
				}
				if err := powfiGet(path+"/current-count", &cursor); err != nil {
					return err
				}
				initialized = true
				log.Printf("Powfi pool=%s watching from cursor=%d interval=%s", address, cursor, interval)
			}
			// Use one price snapshot per polling cycle for consistent valuation.
			price := alphUSDPrice()
			// Keep the cursor unchanged until non-USDT swaps can be valued.
			if quoteToken.ID != powfiUSDT && price <= 0 {
				return fmt.Errorf("ALPH/USD price unavailable; retaining event cursor")
			}
			// The API cursor counts event batches, not individual events.
			for pages := 0; pages < 20; pages++ {
				var page powfiPage
				if err := powfiGet(fmt.Sprintf("%s?start=%d&limit=100", path, cursor), &page); err != nil {
					return err
				}
				if page.NextStart < cursor {
					return fmt.Errorf("event cursor moved backwards: %d -> %d", cursor, page.NextStart)
				}
				if page.NextStart == cursor {
					return nil
				}
				for _, event := range page.Events {
					if event.ContractAddress != address || event.EventIndex != 3 {
						continue
					}
					msg, err := decodePowfiSwap(event, alphIndex, quoteToken, price)
					if err != nil {
						log.Printf("Powfi pool=%s tx=%s invalid swap: %v", address, event.TxID, err)
						continue
					}
					if msg.AmountFiat.Value >= parameters.MinAmountDexTriggerUsd {
						ch <- msg
						cexQueueMetrics.Inc()
						log.Printf("Powfi pool=%s tx=%s side=%s ALPH=%g value=%g %s", address, event.TxID, msg.Side, msg.AmountLeft.Value, msg.AmountFiat.Value, msg.AmountFiat.Symbol)
					}
				}
				cursor = page.NextStart
			}
			return nil
		}()
		if err != nil {
			log.Printf("Powfi pool=%s cursor=%d error: %v; retrying in %s", address, cursor, err, interval)
		}
		time.Sleep(interval)
	}
}

func decodePowfiSwap(event powfiEvent, alphIndex int, quoteToken Token, alphPrice float64) (MessageCex, error) {
	if event.EventIndex != 3 || len(event.Fields) != 7 || alphIndex < 0 || alphIndex > 1 {
		return MessageCex{}, fmt.Errorf("unexpected Swap schema")
	}
	amounts := [2]*big.Int{}
	for i := range amounts {
		field := event.Fields[2+i]
		amount, ok := new(big.Int).SetString(field.Value, 10)
		if field.Type != "I256" || !ok {
			return MessageCex{}, fmt.Errorf("invalid signed amount%d", i)
		}
		amounts[i] = amount
	}
	alph, quote := amounts[alphIndex], amounts[1-alphIndex]
	if alph.Sign()*quote.Sign() != -1 {
		return MessageCex{}, fmt.Errorf("swap amounts must have opposite signs")
	}
	side := "buy"
	if alph.Sign() > 0 {
		side = "sell"
	}
	scaled := func(amount *big.Int, decimals int) float64 {
		value, _ := new(big.Rat).SetFrac(new(big.Int).Abs(amount), new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(decimals)), nil)).Float64()
		return value
	}
	if quoteToken.Decimals < 0 || quoteToken.Decimals > 255 || quoteToken.Symbol == "" {
		return MessageCex{}, fmt.Errorf("invalid quote token metadata")
	}
	a, u := scaled(alph, 18), scaled(quote, quoteToken.Decimals)
	if a <= 0 || u <= 0 || math.IsInf(a, 0) || math.IsInf(u, 0) {
		return MessageCex{}, fmt.Errorf("invalid swap volume")
	}
	value, symbol := u, "USDT"
	if quoteToken.ID != powfiUSDT {
		if alphPrice <= 0 || math.IsNaN(alphPrice) || math.IsInf(alphPrice, 0) {
			return MessageCex{}, fmt.Errorf("ALPH/USD price unavailable")
		}
		value, symbol = a*alphPrice, "USD"
		if math.IsInf(value, 0) {
			return MessageCex{}, fmt.Errorf("invalid USD value")
		}
	}
	return MessageCex{Side: side, AmountLeft: Amount{a, "ALPH"}, AmountFiat: Amount{value, symbol}, QuoteAmount: Amount{u, quoteToken.Symbol}, ExchangeName: "Powfi", Price: u / a, TxID: event.TxID}, nil
}
