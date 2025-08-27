package api

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"sync"
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
	Timestamp        int64   `json:"timestamp"`
	Open             float64 `json:"open"`
	High             float64 `json:"high"`
	Low              float64 `json:"low"`
	Close            float64 `json:"close"`
	Volume           float64 `json:"volume"`
	CloseTime        int64   `json:"close_time"`
	QuoteAssetVolume float64 `json:"quote_asset_volume"`
}

type Trade struct {
	ID           interface{} `json:"id"`
	Price        string      `json:"price"`
	Qty          string      `json:"qty"`
	QuoteQty     string      `json:"quoteQty"`
	Time         int64       `json:"time"`
	IsBuyerMaker bool        `json:"isBuyerMaker"`
	IsBestMatch  bool        `json:"isBestMatch"`
	TradeType    string      `json:"tradeType"`
}

type MEXCClient struct {
	client       *http.Client
	tickersCache *TickersCache
}

type TickersCache struct {
	data      []Ticker
	timestamp time.Time
	mu        sync.RWMutex
}

func NewMEXCClient() *MEXCClient {
	// Оптимизированный HTTP клиент
	transport := &http.Transport{
		MaxIdleConns:        100,
		MaxIdleConnsPerHost: 100,
		IdleConnTimeout:     90 * time.Second,
		DisableKeepAlives:   false, // Включаем keep-alive для переиспользования соединений
	}

	return &MEXCClient{
		client: &http.Client{
			Transport: transport,
			Timeout:   5 * time.Second, // Уменьшаем таймаут до 5 секунд
		},
		tickersCache: &TickersCache{},
	}
}

// GetTickers с кэшированием
func (c *MEXCClient) GetTickers() ([]Ticker, error) {
	// Проверяем кэш (данные актуальны 200мс)
	c.tickersCache.mu.RLock()
	if time.Since(c.tickersCache.timestamp) < 200*time.Millisecond && len(c.tickersCache.data) > 0 {
		cached := c.tickersCache.data
		c.tickersCache.mu.RUnlock()
		return cached, nil
	}
	c.tickersCache.mu.RUnlock()

	// Делаем запрос с контекстом и таймаутом
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, "GET", "https://api.mexc.com/api/v3/ticker/24hr", nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %v", err)
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %v", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("API error: status %d, body: %s", resp.StatusCode, string(body))
	}

	var tickers []Ticker
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&tickers); err != nil {
		return nil, fmt.Errorf("unmarshal error: %v", err)
	}

	// Обновляем кэш
	c.tickersCache.mu.Lock()
	c.tickersCache.data = tickers
	c.tickersCache.timestamp = time.Now()
	c.tickersCache.mu.Unlock()

	return tickers, nil
}

// GetTrades оптимизированный для скорости
func (c *MEXCClient) GetTrades(symbol string, limit int) ([]Trade, error) {
	// Быстрый запрос без повторов
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("limit", fmt.Sprintf("%d", limit))

	urlTrades := "https://api.mexc.com/api/v3/trades?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", urlTrades, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("trades API error: status %d", resp.StatusCode)
	}

	var trades []Trade
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&trades); err != nil {
		return nil, err
	}

	return trades, nil
}

// GetKline с контекстом и таймаутом
func (c *MEXCClient) GetKline(symbol string, startTime, endTime int64) ([]Kline, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	params := url.Values{}
	params.Set("symbol", symbol)
	params.Set("interval", "1m")
	if startTime != 0 {
		params.Set("startTime", fmt.Sprintf("%d", startTime*1000))
	}
	if endTime != 0 {
		params.Set("endTime", fmt.Sprintf("%d", endTime*1000))
	}
	params.Set("limit", "1000")

	urlKline := "https://api.mexc.com/api/v3/klines?" + params.Encode()
	req, err := http.NewRequestWithContext(ctx, "GET", urlKline, nil)
	if err != nil {
		return nil, err
	}

	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("kline API error: status %d", resp.StatusCode)
	}

	var klineData [][]interface{}
	decoder := json.NewDecoder(resp.Body)
	if err := decoder.Decode(&klineData); err != nil {
		return nil, err
	}

	var klines []Kline
	for _, data := range klineData {
		if len(data) < 8 {
			continue
		}

		timestamp, _ := data[0].(float64)
		open, _ := strconv.ParseFloat(data[1].(string), 64)
		high, _ := strconv.ParseFloat(data[2].(string), 64)
		low, _ := strconv.ParseFloat(data[3].(string), 64)
		closePrice, _ := strconv.ParseFloat(data[4].(string), 64)
		volume, _ := strconv.ParseFloat(data[5].(string), 64)
		closeTime, _ := data[6].(float64)
		quoteAssetVolume, _ := strconv.ParseFloat(data[7].(string), 64)

		klines = append(klines, Kline{
			Timestamp:        int64(timestamp) / 1000,
			Open:             open,
			High:             high,
			Low:              low,
			Close:            closePrice,
			Volume:           volume,
			CloseTime:        int64(closeTime) / 1000,
			QuoteAssetVolume: quoteAssetVolume,
		})
	}

	return klines, nil
}
