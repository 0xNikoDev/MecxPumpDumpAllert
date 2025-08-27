package monitor

import (
	"MexcPumpDumpAlert/internal/api"
	"MexcPumpDumpAlert/internal/blacklist"
	"MexcPumpDumpAlert/internal/config"
	"MexcPumpDumpAlert/internal/telegram"
	"fmt"
	"log"
	"math"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// PricePoint хранит цену и время
type PricePoint struct {
	Price     float64
	Timestamp time.Time
}

// PriceHistory хранит историю цен для одной монеты
type PriceHistory struct {
	Symbol string
	Points []PricePoint
	mu     sync.RWMutex
}

// VolumeCache кэш для объемов
type VolumeCache struct {
	data map[string]*VolumeEntry
	mu   sync.RWMutex
}

type VolumeEntry struct {
	Volume    float64
	Timestamp time.Time
}

func NewVolumeCache() *VolumeCache {
	return &VolumeCache{
		data: make(map[string]*VolumeEntry),
	}
}

func (vc *VolumeCache) Get(symbol string, maxAge time.Duration) (float64, bool) {
	vc.mu.RLock()
	defer vc.mu.RUnlock()

	entry, exists := vc.data[symbol]
	if !exists {
		return 0, false
	}

	if time.Since(entry.Timestamp) > maxAge {
		return 0, false
	}

	return entry.Volume, true
}

func (vc *VolumeCache) Set(symbol string, volume float64) {
	vc.mu.Lock()
	defer vc.mu.Unlock()

	vc.data[symbol] = &VolumeEntry{
		Volume:    volume,
		Timestamp: time.Now(),
	}
}

// calculateRequestInterval вычисляет частоту запросов на основе интервала сравнения
func calculateRequestInterval(compareIntervalSeconds int) time.Duration {
	// Для 5-секундного интервала - максимальная частота
	if compareIntervalSeconds <= 5 {
		return 250 * time.Millisecond // 4 запроса в секунду!
	} else if compareIntervalSeconds <= 10 {
		return 500 * time.Millisecond // 2 запроса в секунду
	} else if compareIntervalSeconds <= 30 {
		return 1 * time.Second
	}
	return 2 * time.Second
}

// getEyeEmoji returns an eye emoji based on volume
func getEyeEmoji(volume float64) string {
	if volume >= 1000 && volume < 5000 {
		return "👁️"
	}
	return ""
}

// getFireEmojis returns fire emojis based on volume
func getFireEmojis(volume float64) string {
	if volume < 5000 {
		return ""
	}
	fires := 1 // 5k–9k
	if volume >= 10000 {
		fires = 2 // 10k–19k
	}
	if volume >= 20000 {
		fires = 3 // 20k–29k
	}
	if volume >= 30000 {
		fires = int(math.Min(3+math.Floor((volume-30000)/10000), 10)) // +1 per 10k, max 10
	}
	return strings.Repeat("🔥", fires)
}

// getCircleEmojis returns circle emojis based on price change percentage
func getCircleEmojis(priceChangePct float64) string {
	absPct := math.Abs(priceChangePct)
	circles := int(math.Min(math.Ceil(absPct/10), 10)) // 1 circle per 10%, max 10
	return strings.Repeat("🔵", circles)
}

// calculateVolumeFromTradesFast быстрый расчет объема без повторных попыток
func calculateVolumeFromTradesFast(client *api.MEXCClient, symbol string, intervalSeconds int) (float64, error) {
	// Запрашиваем trades с таймаутом
	trades, err := client.GetTrades(symbol, 1000)
	if err != nil {
		return 0, err
	}

	if len(trades) == 0 {
		return 0, fmt.Errorf("no trades")
	}

	currentTimeMs := time.Now().UnixNano() / int64(time.Millisecond)

	// Для коротких интервалов используем упрощенный расчет
	if intervalSeconds <= 10 {
		// Просто считаем объем за последние N секунд
		intervalMs := int64(intervalSeconds) * 1000
		startTimeMs := currentTimeMs - intervalMs

		var totalVolumeUSD float64
		for _, trade := range trades {
			if trade.Time >= startTimeMs && trade.Time <= currentTimeMs {
				quoteQty, err := strconv.ParseFloat(trade.QuoteQty, 64)
				if err == nil && quoteQty > 0 {
					totalVolumeUSD += quoteQty
				}
			}
		}
		return totalVolumeUSD, nil
	}

	// Для больших интервалов используем скользящие окна
	return calculateMaxVolumeFromSlidingWindows(trades, currentTimeMs, intervalSeconds)
}

// calculateMaxVolumeFromSlidingWindows находит максимальный объем среди скользящих окон
func calculateMaxVolumeFromSlidingWindows(trades []api.Trade, currentTimeMs int64, intervalSeconds int) (float64, error) {
	// Получаем трейды за последние 2 минуты
	twoMinutesMs := int64(120 * 1000)
	startTimeMs := currentTimeMs - twoMinutesMs

	// Создаем структуру для хранения объемов по секундам
	secondVolumes := make(map[int64]float64)

	// Группируем трейды по секундам
	for _, trade := range trades {
		if trade.Time >= startTimeMs && trade.Time <= currentTimeMs {
			quoteQty, err := strconv.ParseFloat(trade.QuoteQty, 64)
			if err == nil && quoteQty > 0 {
				secondTimestamp := trade.Time / 1000
				secondVolumes[secondTimestamp] += quoteQty
			}
		}
	}

	intervalSizeSeconds := int64(intervalSeconds)
	maxVolume := float64(0)

	// Проходим по каждой секунде в диапазоне последних 2 минут
	for i := int64(0); i <= 120-intervalSizeSeconds; i++ {
		windowStartSecond := (currentTimeMs / 1000) - 120 + i
		windowEndSecond := windowStartSecond + intervalSizeSeconds

		windowVolume := float64(0)
		for second := windowStartSecond; second < windowEndSecond; second++ {
			if volume, exists := secondVolumes[second]; exists {
				windowVolume += volume
			}
		}

		if windowVolume > maxVolume {
			maxVolume = windowVolume
		}
	}

	return maxVolume, nil
}

// addPricePoint добавляет новую точку цены и очищает старые (thread-safe)
func (ph *PriceHistory) addPricePoint(price float64, timestamp time.Time, keepDuration time.Duration) {
	ph.mu.Lock()
	defer ph.mu.Unlock()

	ph.Points = append(ph.Points, PricePoint{
		Price:     price,
		Timestamp: timestamp,
	})

	// Очищаем старые точки
	cutoffTime := timestamp.Add(-keepDuration - 10*time.Second)
	var newPoints []PricePoint
	for _, point := range ph.Points {
		if point.Timestamp.After(cutoffTime) {
			newPoints = append(newPoints, point)
		}
	}
	ph.Points = newPoints
}

// findPriceExtremes находит мин/макс цены в определенном временном диапазоне (thread-safe)
func (ph *PriceHistory) findPriceExtremes(fromTime, toTime time.Time) (minPrice, maxPrice float64, found bool) {
	ph.mu.RLock()
	defer ph.mu.RUnlock()

	var prices []float64
	for _, point := range ph.Points {
		if point.Timestamp.After(fromTime) && point.Timestamp.Before(toTime) {
			prices = append(prices, point.Price)
		}
	}

	if len(prices) == 0 {
		return 0, 0, false
	}

	minPrice = prices[0]
	maxPrice = prices[0]
	for _, price := range prices {
		if price < minPrice {
			minPrice = price
		}
		if price > maxPrice {
			maxPrice = price
		}
	}

	return minPrice, maxPrice, true
}

// getPointsCount возвращает количество точек в истории (thread-safe)
func (ph *PriceHistory) getPointsCount() int {
	ph.mu.RLock()
	defer ph.mu.RUnlock()
	return len(ph.Points)
}

// processTicker обрабатывает один тикер БЫСТРО
func processTicker(
	ticker api.Ticker,
	cfg *config.Config,
	bl *blacklist.Blacklist,
	bot *telegram.Bot,
	client *api.MEXCClient,
	priceHistories map[string]*PriceHistory,
	historyMutex *sync.RWMutex,
	volumeCache *VolumeCache,
	processedCount *int32,
	alertCount *int32,
	intervalDuration time.Duration,
	volumeWg *sync.WaitGroup,
	volumeSemaphore chan struct{},
) {
	currentPrice, err := strconv.ParseFloat(ticker.Price, 64)
	if err != nil || currentPrice <= 0 {
		return
	}

	atomic.AddInt32(processedCount, 1)
	currentTime := time.Now()

	// Получаем или создаем историю для этой монеты
	historyMutex.RLock()
	history, exists := priceHistories[ticker.Symbol]
	historyMutex.RUnlock()

	if !exists {
		history = &PriceHistory{Symbol: ticker.Symbol}
		historyMutex.Lock()
		priceHistories[ticker.Symbol] = history
		historyMutex.Unlock()
	}

	// Добавляем текущую цену в историю
	history.addPricePoint(currentPrice, currentTime, intervalDuration)

	// Проверяем, есть ли достаточно данных для анализа
	if history.getPointsCount() < 2 {
		return
	}

	// Минимальный буфер для 5-секундного интервала
	bufferSeconds := 1
	if cfg.IntervalSeconds > 10 {
		bufferSeconds = 2
	}

	compareFromTime := currentTime.Add(-intervalDuration - time.Duration(bufferSeconds)*time.Second)
	compareToTime := currentTime.Add(-intervalDuration + time.Duration(bufferSeconds)*time.Second)

	// Находим минимальную и максимальную цены
	minPrice, maxPrice, found := history.findPriceExtremes(compareFromTime, compareToTime)
	if !found {
		return
	}

	// Рассчитываем изменения
	pumpChangePct := ((currentPrice - minPrice) / minPrice) * 100
	dumpChangePct := ((currentPrice - maxPrice) / maxPrice) * 100

	var significantChangePct float64
	var changeType string
	var referencePrice float64

	if math.Abs(pumpChangePct) > math.Abs(dumpChangePct) {
		significantChangePct = pumpChangePct
		changeType = "PUMP"
		referencePrice = minPrice
	} else {
		significantChangePct = dumpChangePct
		changeType = "DUMP"
		referencePrice = maxPrice
	}

	// Проверяем порог изменения цены
	if math.Abs(significantChangePct) >= cfg.PriceChangePct {
		// Проверяем кэш объема
		cachedVolume, hasCache := volumeCache.Get(ticker.Symbol, 2*time.Second)

		if hasCache && cachedVolume >= cfg.VolumeUSD {
			// Используем кэшированный объем - мгновенный алерт!
			sendAlert(ticker, significantChangePct, cachedVolume, referencePrice, currentPrice, changeType, bot, bl, alertCount)
		} else {
			// Запускаем асинхронную проверку объема
			volumeWg.Add(1)
			go func() {
				defer volumeWg.Done()

				// Пытаемся захватить семафор с таймаутом
				select {
				case volumeSemaphore <- struct{}{}:
					defer func() { <-volumeSemaphore }()

					volume, err := calculateVolumeFromTradesFast(client, ticker.Symbol, cfg.IntervalSeconds)
					if err == nil {
						volumeCache.Set(ticker.Symbol, volume)

						if volume >= cfg.VolumeUSD {
							sendAlert(ticker, significantChangePct, volume, referencePrice, currentPrice, changeType, bot, bl, alertCount)
						}
					}
				case <-time.After(100 * time.Millisecond):
					// Пропускаем если семафор занят
				}
			}()
		}
	}
}

// sendAlert отправляет алерт
func sendAlert(ticker api.Ticker, changePct, volume, refPrice, curPrice float64, changeType string, bot *telegram.Bot, bl *blacklist.Blacklist, alertCount *int32) {
	directionEmoji := "🟢"
	if changePct < 0 {
		directionEmoji = "🔴"
	}

	circleEmojis := getCircleEmojis(changePct)
	eyeEmoji := getEyeEmoji(volume)
	fireEmojis := getFireEmojis(volume)

	msg := fmt.Sprintf(
		"%s %s\n%.2f%% %s\n$%.0f %s%s",
		strings.ToUpper(ticker.Symbol), directionEmoji,
		math.Abs(changePct), circleEmojis,
		volume, eyeEmoji, fireEmojis,
	)

	log.Printf("🚨 %s: %s %.8f->%.8f (%.2f%%), Vol: $%.0f",
		changeType, ticker.Symbol, refPrice, curPrice, changePct, volume)

	bot.SendMessage(msg)
	bl.Add(ticker.Symbol, 10*time.Minute)
	atomic.AddInt32(alertCount, 1)
}

func Run(client *api.MEXCClient, cfg *config.Config, bl *blacklist.Blacklist, bot *telegram.Bot) {
	// Инициализация
	priceHistories := make(map[string]*PriceHistory)
	historyMutex := &sync.RWMutex{}
	volumeCache := NewVolumeCache()

	// Увеличиваем количество параллельных запросов для volume
	volumeSemaphore := make(chan struct{}, 20)

	// Счетчики
	var totalCycles int64
	var lastLogTime time.Time

	log.Printf("🚀 Monitor started (interval: %ds, threshold: %.2f%%, volume: $%.0f)",
		cfg.IntervalSeconds, cfg.PriceChangePct, cfg.VolumeUSD)

	// Основной цикл
	for {
		cycleStart := time.Now()
		atomic.AddInt64(&totalCycles, 1)

		// Частота обновления
		requestInterval := calculateRequestInterval(cfg.IntervalSeconds)

		// Получаем тикеры
		tickers, err := client.GetTickers()
		if err != nil {
			log.Printf("❌ Error fetching tickers: %v", err)
			time.Sleep(500 * time.Millisecond)
			continue
		}

		var processedCount int32
		var alertCount int32
		intervalDuration := time.Duration(cfg.IntervalSeconds) * time.Second

		// WaitGroup для отслеживания асинхронных проверок объема
		var volumeWg sync.WaitGroup

		// Обрабатываем все тикеры ПАРАЛЛЕЛЬНО и БЫСТРО
		var wg sync.WaitGroup
		semaphore := make(chan struct{}, 50) // Увеличиваем параллелизм

		for _, ticker := range tickers {
			if bl.IsBlacklisted(ticker.Symbol) {
				continue
			}

			wg.Add(1)
			go func(t api.Ticker) {
				defer wg.Done()

				semaphore <- struct{}{}
				defer func() { <-semaphore }()

				processTicker(
					t, cfg, bl, bot, client,
					priceHistories, historyMutex,
					volumeCache,
					&processedCount, &alertCount,
					intervalDuration,
					&volumeWg, volumeSemaphore,
				)
			}(ticker)
		}

		// Ждем обработку всех тикеров
		wg.Wait()

		// Ждем завершения проверок объема с таймаутом
		volumeDone := make(chan bool)
		go func() {
			volumeWg.Wait()
			volumeDone <- true
		}()

		select {
		case <-volumeDone:
			// Все проверки завершены
		case <-time.After(requestInterval - 50*time.Millisecond):
			// Таймаут - идем дальше
		}

		// Очистка старых историй (каждые 30 циклов)
		if atomic.LoadInt64(&totalCycles)%30 == 0 {
			go func() {
				historyMutex.Lock()
				defer historyMutex.Unlock()

				cleanupThreshold := time.Now().Add(-2 * intervalDuration)
				for symbol, history := range priceHistories {
					if history.getPointsCount() == 0 {
						delete(priceHistories, symbol)
						continue
					}
					history.mu.RLock()
					needDelete := len(history.Points) > 0 && history.Points[len(history.Points)-1].Timestamp.Before(cleanupThreshold)
					history.mu.RUnlock()
					if needDelete {
						delete(priceHistories, symbol)
					}
				}
			}()
		}

		// Статистика
		elapsed := time.Since(cycleStart)
		currentAlertCount := atomic.LoadInt32(&alertCount)
		currentProcessedCount := atomic.LoadInt32(&processedCount)

		if currentAlertCount > 0 {
			log.Printf("🚨 Cycle #%d: %d alerts, %d tickers, %.3fs",
				totalCycles, currentAlertCount, currentProcessedCount, elapsed.Seconds())
			lastLogTime = time.Now()
		} else if time.Since(lastLogTime) > 30*time.Second {
			log.Printf("✅ Cycle #%d: %d tickers, %.3fs",
				totalCycles, currentProcessedCount, elapsed.Seconds())
			lastLogTime = time.Now()
		}

		// Минимальная задержка между циклами
		if elapsed < requestInterval {
			time.Sleep(requestInterval - elapsed)
		}
	}
}
