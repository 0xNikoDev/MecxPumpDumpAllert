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
}

// calculateRequestInterval вычисляет частоту запросов на основе интервала сравнения
func calculateRequestInterval(compareIntervalSeconds int) int {
	// Формула: max(1, min(3, интервал_сравнения / 20))
	requestInterval := compareIntervalSeconds / 20
	if requestInterval < 1 {
		requestInterval = 1
	}
	if requestInterval > 3 {
		requestInterval = 3
	}
	return requestInterval
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
	// Добавляем буфер 10 секунд на время запроса
	bufferMs := int64(10 * 1000)
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
			// Если нет трейдов в секунде, объем остается 0 (не добавляем ничего)
		}

		if windowVolume > maxVolume {
			maxVolume = windowVolume
		}
	}

	return maxVolume, nil
}

// calculateVolumeUSD рассчитывает максимальный объем из скользящих окон или общий объем для больших интервалов
func calculateVolumeUSD(client *api.MEXCClient, ticker api.Ticker, currentPrice float64, intervalSeconds int) float64 {
	// Попытки получить объем из trades
	for attempt := 1; attempt <= 3; attempt++ {
		volume, err := calculateVolumeFromTrades(client, ticker.Symbol, intervalSeconds)
		if err == nil {
			return volume
		}

		// Не логируем каждую неудачную попытку, чтобы не спамить логи
		if attempt < 3 {
			time.Sleep(100 * time.Millisecond)
		}
	}

	// Если все попытки неудачны, возвращаем 0 (будет отклонено по минимальному объему)
	return 0
}

// addPricePoint добавляет новую точку цены и очищает старые
func (ph *PriceHistory) addPricePoint(price float64, timestamp time.Time, keepDuration time.Duration) {
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

// findPriceExtremes находит мин/макс цены в определенном временном диапазоне
func (ph *PriceHistory) findPriceExtremes(fromTime, toTime time.Time) (minPrice, maxPrice float64, found bool) {
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

func Run(client *api.MEXCClient, cfg *config.Config, bl *blacklist.Blacklist, bot *telegram.Bot) {
	// Храним историю цен для каждой монеты
	priceHistories := make(map[string]*PriceHistory)

	for {
		startTime := time.Now()

		// Вычисляем динамическую частоту запросов
		requestIntervalSeconds := calculateRequestInterval(cfg.IntervalSeconds)
		log.Printf("🔄 Starting cycle (compare interval: %ds, request interval: %ds)",
			cfg.IntervalSeconds, requestIntervalSeconds)

		tickers, err := client.GetTickers()
		if err != nil {
			log.Printf("❌ Error fetching tickers: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		log.Printf("📊 Fetched %d tickers", len(tickers))

		processedCount := 0
		alertCount := 0
		intervalDuration := time.Duration(cfg.IntervalSeconds) * time.Second

		for _, ticker := range tickers {
			if bl.IsBlacklisted(ticker.Symbol) {
				continue
			}

			currentPrice, err := strconv.ParseFloat(ticker.Price, 64)
			if err != nil || currentPrice <= 0 {
				continue
			}

			processedCount++
			currentTime := time.Now()

			// Получаем или создаем историю для этой монеты
			history, exists := priceHistories[ticker.Symbol]
			if !exists {
				history = &PriceHistory{Symbol: ticker.Symbol}
				priceHistories[ticker.Symbol] = history
			}

			// Добавляем текущую цену в историю
			history.addPricePoint(currentPrice, currentTime, intervalDuration)

			// Проверяем, есть ли достаточно данных для анализа
			if len(history.Points) < 2 {
				continue
			}

			// Определяем временной диапазон для сравнения
			// Ищем цены от (intervalSeconds - буфер) до (intervalSeconds + буфер) секунд назад
			bufferSeconds := int(math.Max(3, float64(cfg.IntervalSeconds)*0.1)) // 10% от интервала, минимум 3 сек
			compareFromTime := currentTime.Add(-intervalDuration - time.Duration(bufferSeconds)*time.Second)
			compareToTime := currentTime.Add(-intervalDuration + time.Duration(bufferSeconds)*time.Second)

			// Находим минимальную и максимальную цены в этом диапазоне
			minPrice, maxPrice, found := history.findPriceExtremes(compareFromTime, compareToTime)
			if !found {
				continue
			}

			// Рассчитываем изменения для пампа (от минимума) и дампа (от максимума)
			pumpChangePct := ((currentPrice - minPrice) / minPrice) * 100 // Рост от минимума
			dumpChangePct := ((currentPrice - maxPrice) / maxPrice) * 100 // Падение от максимума

			// Определяем, какое изменение более значительное
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

			// Проверяем, превышает ли изменение пороговое значение
			if math.Abs(significantChangePct) >= cfg.PriceChangePct {
				log.Printf("🎯 POTENTIAL %s: %s %.8f->%.8f (%.2f%%)",
					changeType, ticker.Symbol, referencePrice, currentPrice, significantChangePct)

				// Рассчитываем точный объем из trades
				volumeUSD := calculateVolumeUSD(client, ticker, currentPrice, cfg.IntervalSeconds)

				if volumeUSD > 0 {
					if cfg.IntervalSeconds <= 120 {
						log.Printf("📊 Max sliding window volume for %s: $%.2f (required: $%.2f, interval: %ds)",
							ticker.Symbol, volumeUSD, cfg.VolumeUSD, cfg.IntervalSeconds)
					} else {
						log.Printf("📊 Total interval volume for %s: $%.2f (required: $%.2f, interval: %ds)",
							ticker.Symbol, volumeUSD, cfg.VolumeUSD, cfg.IntervalSeconds)
					}
				} else {
					log.Printf("📊 No volume data for %s (trades API failed)", ticker.Symbol)
				}

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

					log.Printf("🚨 ALERT SENT: %s", msg)
					bot.SendMessage(msg)
					bl.Add(ticker.Symbol, 10*time.Minute)
					alertCount++
				} else {
					log.Printf("💰 Volume too low: %s $%.2f < $%.2f",
						ticker.Symbol, volumeUSD, cfg.VolumeUSD)
				}
			}
		}

		// Очистка старых историй
		cleanedCount := 0
		cleanupThreshold := time.Now().Add(-2 * intervalDuration)
		for symbol, history := range priceHistories {
			if len(history.Points) == 0 || history.Points[len(history.Points)-1].Timestamp.Before(cleanupThreshold) {
				delete(priceHistories, symbol)
				cleanedCount++
			}
		}

		elapsed := time.Since(startTime)

		// Логируем только если были алерты или есть что-то интересное
		if alertCount > 0 {
			log.Printf("🚨 CYCLE SUMMARY: %d ALERTS sent from %d processed tickers in %v",
				alertCount, processedCount, elapsed)
		} else if processedCount > 0 && processedCount%100 == 0 {
			// Периодически показываем, что система работает
			log.Printf("✅ System active: processed %d tickers, no alerts in %v",
				processedCount, elapsed)
		}

		// Ждем следующего запроса (динамический интервал)
		requestInterval := time.Duration(requestIntervalSeconds) * time.Second
		if elapsed < requestInterval {
			time.Sleep(requestInterval - elapsed)
		} else {
			log.Printf("⚠️ Warning: Cycle took longer than request interval (%v > %v)", elapsed, requestInterval)
		}
	}
}
