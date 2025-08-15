package api

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

type Ticker struct {
	Symbol      string `json:"symbol"`
	Price       string `json:"lastPrice"`
	Volume24h   string `json:"volume"`
	QuoteVol24h string `json:"quoteVolume"`
	Open24h     string `json:"openPrice"`
	High24h     string `json:"highPrice"`
	Low24h      string `json:"lowPrice"`
	Change      string `json:"priceChange"`
	ChangePct   string `json:"priceChangePercent"`
	BidPrice    string `json:"bidPrice"`
	BidQty      string `json:"bidQty"`
	AskPrice    string `json:"askPrice"`
	AskQty      string `json:"askQty"`
	OpenTime    int64  `json:"openTime"`
	CloseTime   int64  `json:"closeTime"`
	Count       int64  `json:"count"`
}

type Kline struct {
	Timestamp int64   `json:"timestamp"`
	Open      float64 `json:"open"`
	High      float64 `json:"high"`
	Low       float64 `json:"low"`
	Close     float64 `json:"close"`
	Volume    float64 `json:"volume"`
}

type MEXCClient struct {
	client *http.Client
}

func NewMEXCClient() *MEXCClient {
	return &MEXCClient{
		client: &http.Client{
			Timeout: 10 * time.Second,
		},
	}
}

func (c *MEXCClient) GetTickers() ([]Ticker, error) {
	for attempt := 1; attempt <= 3; attempt++ {
		time.Sleep(100 * time.Millisecond)

		req, err := http.NewRequest("GET", "https://api.mexc.com/api/v3/ticker/24hr", nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create request: %v", err)
		}

		resp, err := c.client.Do(req)
		if err != nil {
			log.Printf("Attempt %d: request failed: %v", attempt, err)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read response: %v", err)
		}

		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("API error: status %d, body: %s", resp.StatusCode, string(body))
		}

		var tickers []Ticker
		if err := json.Unmarshal(body, &tickers); err != nil {
			return nil, fmt.Errorf("unmarshal error: %v, body: %s", err, string(body))
		}

		log.Printf("GetTickers: %d tickers", len(tickers))
		return tickers, nil
	}

	return nil, fmt.Errorf("failed to fetch tickers after 3 attempts")
}

func (c *MEXCClient) GetKline(symbol string, startTime, endTime int64) ([]Kline, error) {
	for attempt := 1; attempt <= 3; attempt++ {
		time.Sleep(100 * time.Millisecond)

		params := url.Values{}
		params.Set("symbol", symbol)
		params.Set("interval", "1m") // 1 minute interval
		if startTime != 0 {
			params.Set("startTime", fmt.Sprintf("%d", startTime*1000)) // Convert to milliseconds
		}
		if endTime != 0 {
			params.Set("endTime", fmt.Sprintf("%d", endTime*1000))
		}
		params.Set("limit", "1000")

		urlKline := "https://api.mexc.com/api/v3/klines?" + params.Encode()
		req, err := http.NewRequest("GET", urlKline, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create kline request: %v", err)
		}

		resp, err := c.client.Do(req)
		if err != nil {
			log.Printf("Attempt %d: kline request failed: %v", attempt, err)
			continue
		}

		body, err := io.ReadAll(resp.Body)
		resp.Body.Close()
		if err != nil {
			return nil, fmt.Errorf("failed to read kline response: %v", err)
		}

		if resp.StatusCode != 200 {
			return nil, fmt.Errorf("kline API error: status %d, body: %s", resp.StatusCode, string(body))
		}

		// MEXC klines response format: [[timestamp, open, high, low, close, volume, close_time, quote_asset_volume], ...]
		var klineData [][]interface{}
		if err := json.Unmarshal(body, &klineData); err != nil {
			return nil, fmt.Errorf("unmarshal kline error: %v, body: %s", err, string(body))
		}

		var klines []Kline
		for _, data := range klineData {
			if len(data) < 8 {
				log.Printf("Warning: incomplete kline data for %s: %v", symbol, data)
				continue
			}

			timestamp, _ := data[0].(float64)
			open, _ := strconv.ParseFloat(data[1].(string), 64)
			high, _ := strconv.ParseFloat(data[2].(string), 64)
			low, _ := strconv.ParseFloat(data[3].(string), 64)
			closePrice, _ := strconv.ParseFloat(data[4].(string), 64)
			volume, _ := strconv.ParseFloat(data[5].(string), 64)

			klines = append(klines, Kline{
				Timestamp: int64(timestamp) / 1000, // Convert from milliseconds to seconds
				Open:      open,
				High:      high,
				Low:       low,
				Close:     closePrice,
				Volume:    volume,
			})
		}

		log.Printf("GetKline: %d klines for %s", len(klines), symbol)
		return klines, nil
	}

	return nil, fmt.Errorf("failed to fetch klines for %s after 3 attempts", symbol)
}
