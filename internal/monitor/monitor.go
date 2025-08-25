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

// calculateRequestInterval вычисляет частоту запросов на основе интервала сравнения
func calculateRequestInterval(compareIntervalSeconds int) time.Duration {
	// Оптимизация для 5-секундного интервала - делаем запросы каждые 500мс
	if compareIntervalSeconds <= 10 {
		return 500 * time.Millisecond // 2 запроса в секунду для интервалов до 10 секунд
	} else if compareIntervalSeconds <= 30 {
		return 1 * time.Second // Запрос каждую секунду для интервалов 10-30 секунд
	} else if compareIntervalSeconds <= 60 {
		return 1500 * time.Millisecond // Запрос каждые 1.5 секунды для интервалов 30-60 секунд
	}
	return 2 * time.Second // Запрос каждые 2 секунды для больших интервалов
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

// calculateVolumeFromTrades рассчитывает максимальный объем из скользящих окон или общий объем для больших интервалов
func calculateVolumeFromTrades(client *api.MEXCClient, symbol string, intervalSeconds int) (float64, error) {
	// Запрашиваем последние trades (максимум 1000)
	trades, err := client.GetTrades(symbol, 1000)
	if err != nil {
		return 0, fmt.Errorf("failed to get trades: %v", err)
	}

	if len(trades) == 0 {
		return 0, fmt.Errorf("no trades data")
	}

	currentTimeMs := time.Now().UnixNano() / int64(time.Millisecond)

	// Если интервал больше 2 минут, используем простую логику - весь интервал + буфер
	if intervalSeconds > 120 {
		return calculateTotalVolumeForLargeInterval(trades, currentTimeMs, intervalSeconds)
	}

	// Для интервалов <= 2 минуты используем скользящие окна за последние 2 минуты
	return calculateMaxVolumeFromSlidingWindows(trades, currentTimeMs, intervalSeconds)
}

// calculateTotalVolumeForLargeInterval рассчитывает общий объем для больших интервалов
func calculateTotalVolumeForLargeInterval(trades []api.Trade, currentTimeMs int64, intervalSeconds int) (float64, error) {
	// Уменьшаем буфер до 3 секунд для более точных результатов
	bufferMs := int64(3 * 1000)
	intervalMs := int64(intervalSeconds) * 1000
	startTimeMs := currentTimeMs - intervalMs - bufferMs

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

// calculateMaxVolumeFromSlidingWindows находит максимальный объем среди скользящих окон за 2 минуты
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
				// Округляем время до секунд
				secondTimestamp := trade.Time / 1000
				secondVolumes[secondTimestamp] += quoteQty
			}
		}
	}

	// Создаем скользящие окна и находим максимальный объем
	intervalSizeSeconds := int64(intervalSeconds)
	maxVolume := float64(0)

	// Проходим по каждой секунде в диапазоне последних 2 минут
	for i := int64(0); i <= 120-intervalSizeSeconds; i++ {
		windowStartSecond := (currentTimeMs / 1000) - 120 + i
		windowEndSecond := windowStartSecond + intervalSizeSeconds

		windowVolume := float64(0)

		// Суммируем объемы в текущем окне
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

// calculateVolumeUSD рассчитывает объем с оптимизацией для коротких интервалов
func calculateVolumeUSD(client *api.MEXCClient, ticker api.Ticker, currentPrice float64, intervalSeconds int) float64 {
	// Для очень коротких интервалов (5 секунд) делаем только 1 попытку чтобы не задерживать
	maxAttempts := 1
	if intervalSeconds > 30 {
		maxAttempts = 2
	}

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		volume, err := calculateVolumeFromTrades(client, ticker.Symbol, intervalSeconds)
		if err == nil {
			return volume
		}

		if attempt < maxAttempts {
			time.Sleep(30 * time.Millisecond) // Минимальная задержка
		}
	}

	return 0
}

// addPricePoint добавляет новую точку цены и очищает старые (thread-safe)
func (ph *PriceHistory) addPricePoint(price float64, timestamp time.Time, keepDuration time.Duration) {
	ph.mu.Lock()
	defer ph.mu.Unlock()

	// Добавляем новую точку
	ph.Points = append(ph.Points, PricePoint{
		Price:     price,
		Timestamp: timestamp,
	})

	// Очищаем старые точки (оставляем данные за keepDuration + буфер)
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

// processTicker обрабатывает один тикер
func processTicker(
	ticker api.Ticker,
	cfg *config.Config,
	bl *blacklist.Blacklist,
	bot *telegram.Bot,
	client *api.MEXCClient,
	priceHistories map[string]*PriceHistory,
	historyMutex *sync.RWMutex,
	processedCount *int32,
	alertCount *int32,
	intervalDuration time.Duration,
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

	// Для 5-секундного интервала используем минимальный буфер
	bufferSeconds := 1
	if cfg.IntervalSeconds > 10 {
		bufferSeconds = int(math.Max(2, float64(cfg.IntervalSeconds)*0.1))
	}

	compareFromTime := currentTime.Add(-intervalDuration - time.Duration(bufferSeconds)*time.Second)
	compareToTime := currentTime.Add(-intervalDuration + time.Duration(bufferSeconds)*time.Second)

	// Находим минимальную и максимальную цены в этом диапазоне
	minPrice, maxPrice, found := history.findPriceExtremes(compareFromTime, compareToTime)
	if !found {
		return
	}

	// Рассчитываем изменения для пампа и дампа
	pumpChangePct := ((currentPrice - minPrice) / minPrice) * 100
	dumpChangePct := ((currentPrice - maxPrice) / maxPrice) * 100

	// Определяем, какое изменение более значительное
	var significantChangePct float64
	var referencePrice float64

	if math.Abs(pumpChangePct) > math.Abs(dumpChangePct) {
		significantChangePct = pumpChangePct
		referencePrice = minPrice
	} else {
		significantChangePct = dumpChangePct
		referencePrice = maxPrice
	}

	// Проверяем, превышает ли изменение пороговое значение
	if math.Abs(significantChangePct) >= cfg.PriceChangePct {
		// Рассчитываем точный объем из trades
		volumeUSD := calculateVolumeUSD(client, ticker, currentPrice, cfg.IntervalSeconds)

		// Проверяем объем
		if volumeUSD >= cfg.VolumeUSD {
			directionEmoji := "🟢"
			if significantChangePct < 0 {
				directionEmoji = "🔴"
			}

			circleEmojis := getCircleEmojis(significantChangePct)
			eyeEmoji := getEyeEmoji(volumeUSD)
			fireEmojis := getFireEmojis(volumeUSD)

			msg := fmt.Sprintf(
				"%s %s\n%.2f%% %s\n$%.0f %s%s",
				strings.ToUpper(ticker.Symbol), directionEmoji,
				math.Abs(significantChangePct), circleEmojis,
				volumeUSD, eyeEmoji, fireEmojis,
			)

			log.Printf("🚨 ALERT: %s (%.8f->%.8f)", ticker.Symbol, referencePrice, currentPrice)
			bot.SendMessage(msg)
			bl.Add(ticker.Symbol, 10*time.Minute)
			atomic.AddInt32(alertCount, 1)
		}
	}
}

func Run(client *api.MEXCClient, cfg *config.Config, bl *blacklist.Blacklist, bot *telegram.Bot) {
	// Храним историю цен для каждой монеты
	priceHistories := make(map[string]*PriceHistory)
	historyMutex := &sync.RWMutex{}

	// Канал для ограничения параллельных запросов к API
	// Для 5-секундного интервала увеличиваем до 15 параллельных запросов
	semaphore := make(chan struct{}, 15)

	// Статистика для мониторинга производительности
	var lastLogTime time.Time
	var totalCycles int64

	log.Printf("🚀 Monitor started (interval: %ds, threshold: %.2f%%, volume: $%.0f)",
		cfg.IntervalSeconds, cfg.PriceChangePct, cfg.VolumeUSD)

	for {
		startTime := time.Now()
		atomic.AddInt64(&totalCycles, 1)

		// Вычисляем динамическую частоту запросов
		requestInterval := calculateRequestInterval(cfg.IntervalSeconds)

		tickers, err := client.GetTickers()
		if err != nil {
			log.Printf("❌ Error fetching tickers: %v", err)
			time.Sleep(1 * time.Second)
			continue
		}

		var processedCount int32
		var alertCount int32
		intervalDuration := time.Duration(cfg.IntervalSeconds) * time.Second

		// Используем WaitGroup для параллельной обработки
		var wg sync.WaitGroup

		// Обрабатываем тикеры параллельно
		for _, ticker := range tickers {
			if bl.IsBlacklisted(ticker.Symbol) {
				continue
			}

			wg.Add(1)
			go func(t api.Ticker) {
				defer wg.Done()

				// Ограничиваем количество параллельных запросов
				select {
				case semaphore <- struct{}{}:
					defer func() { <-semaphore }()
					processTicker(
						t, cfg, bl, bot, client,
						priceHistories, historyMutex,
						&processedCount, &alertCount,
						intervalDuration,
					)
				case <-time.After(50 * time.Millisecond):
					// Пропускаем если семафор занят слишком долго
					return
				}
			}(ticker)
		}

		// Ждем завершения обработки всех тикеров с таймаутом
		done := make(chan bool)
		go func() {
			wg.Wait()
			done <- true
		}()

		select {
		case <-done:
			// Все обработано
		case <-time.After(requestInterval - 100*time.Millisecond):
			// Таймаут - переходим к следующему циклу
		}

		// Очистка старых историй (делаем реже - каждые 20 циклов)
		if atomic.LoadInt64(&totalCycles)%20 == 0 {
			historyMutex.Lock()
			cleanupThreshold := time.Now().Add(-2 * intervalDuration)
			for symbol, history := range priceHistories {
				if history.getPointsCount() == 0 {
					delete(priceHistories, symbol)
					continue
				}
				history.mu.RLock()
				lastPoint := len(history.Points) > 0 && history.Points[len(history.Points)-1].Timestamp.Before(cleanupThreshold)
				history.mu.RUnlock()
				if lastPoint {
					delete(priceHistories, symbol)
				}
			}
			historyMutex.Unlock()
		}

		elapsed := time.Since(startTime)
		currentAlertCount := atomic.LoadInt32(&alertCount)
		currentProcessedCount := atomic.LoadInt32(&processedCount)

		// Логируем статистику
		if currentAlertCount > 0 {
			log.Printf("🚨 Cycle #%d: %d alerts, %d tickers, %.2fs",
				totalCycles, currentAlertCount, currentProcessedCount, elapsed.Seconds())
			lastLogTime = time.Now()
		} else if time.Since(lastLogTime) > 30*time.Second {
			// Показываем статус каждые 30 секунд
			log.Printf("✅ Cycle #%d: %d tickers, %.2fs",
				totalCycles, currentProcessedCount, elapsed.Seconds())
			lastLogTime = time.Now()
		}

		// Ждем следующего запроса
		if elapsed < requestInterval {
			time.Sleep(requestInterval - elapsed)
		}
	}
}
