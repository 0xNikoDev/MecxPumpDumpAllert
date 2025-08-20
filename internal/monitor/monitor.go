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

// calculateVolumeFrom24h рассчитывает объем на основе 24-часового объема
func calculateVolumeFrom24h(ticker api.Ticker, currentPrice float64, intervalSeconds int) float64 {
	// Метод 1: Используем QuoteVolume24h (уже в USD/USDT)
	quoteVolume24h, err := strconv.ParseFloat(ticker.QuoteVol24h, 64)
	if err == nil && quoteVolume24h > 0 {
		// Пропорциональное распределение
		intervalFraction := float64(intervalSeconds) / (24 * 60 * 60) // доля от 24 часов
		baseVolume := quoteVolume24h * intervalFraction

		// Корректировка на волатильность
		changePct24h, changePctErr := strconv.ParseFloat(ticker.ChangePct, 64)
		volatilityMultiplier := 1.0
		if changePctErr == nil {
			absChangePct := math.Abs(changePct24h)
			if absChangePct >= 20 {
				volatilityMultiplier = 4.0
			} else if absChangePct >= 15 {
				volatilityMultiplier = 3.0
			} else if absChangePct >= 10 {
				volatilityMultiplier = 2.0
			} else if absChangePct >= 5 {
				volatilityMultiplier = 1.5
			}
		}

		return baseVolume * volatilityMultiplier
	}

	// Fallback: используем Volume24h * currentPrice
	volume24h, err := strconv.ParseFloat(ticker.Volume24h, 64)
	if err == nil && volume24h > 0 && currentPrice > 0 {
		intervalFraction := float64(intervalSeconds) / (24 * 60 * 60)
		baseVolume := (volume24h * currentPrice) * intervalFraction

		// Применяем волатильность
		changePct24h, changePctErr := strconv.ParseFloat(ticker.ChangePct, 64)
		volatilityMultiplier := 1.0
		if changePctErr == nil {
			absChangePct := math.Abs(changePct24h)
			if absChangePct >= 20 {
				volatilityMultiplier = 4.0
			} else if absChangePct >= 15 {
				volatilityMultiplier = 3.0
			} else if absChangePct >= 10 {
				volatilityMultiplier = 2.0
			} else if absChangePct >= 5 {
				volatilityMultiplier = 1.5
			}
		}

		return baseVolume * volatilityMultiplier
	}

	return 0
}

// calculateVolumeFromKlines рассчитывает объем из klines за точный интервал
func calculateVolumeFromKlines(client *api.MEXCClient, symbol string, intervalSeconds int) float64 {
	endTime := time.Now().Unix()
	startTime := endTime - int64(intervalSeconds) - 60 // добавляем буфер в 60 сек

	klines, err := client.GetKline(symbol, startTime, endTime)
	if err != nil {
		return 0
	}

	if len(klines) == 0 {
		return 0
	}

	// Фильтруем klines за точный интервал
	targetStartTime := endTime - int64(intervalSeconds)
	var totalVolumeUSD float64
	var validKlines int

	for _, kline := range klines {
		// Проверяем, что kline попадает в наш интервал
		if kline.Timestamp >= targetStartTime && kline.Timestamp <= endTime {
			// Используем QuoteAssetVolume (уже в USD/USDT)
			if kline.QuoteAssetVolume > 0 {
				totalVolumeUSD += kline.QuoteAssetVolume
				validKlines++
			} else if kline.Volume > 0 && kline.Close > 0 {
				// Fallback: базовый объем * цена закрытия
				totalVolumeUSD += kline.Volume * kline.Close
				validKlines++
			}
		}
	}

	if validKlines > 0 {
		log.Printf("🔍 Klines volume for %s: $%.2f from %d valid klines", symbol, totalVolumeUSD, validKlines)
	}

	return totalVolumeUSD
}

// calculateVolumeUSD рассчитывает объем двумя методами и возвращает максимальный
func calculateVolumeUSD(client *api.MEXCClient, ticker api.Ticker, currentPrice float64, intervalSeconds int) float64 {
	// Метод 1: из 24-часового объема
	volume24h := calculateVolumeFrom24h(ticker, currentPrice, intervalSeconds)

	// Метод 2: из klines за точный интервал
	volumeKlines := calculateVolumeFromKlines(client, ticker.Symbol, intervalSeconds)

	// Возвращаем максимальный
	finalVolume := math.Max(volume24h, volumeKlines)

	if volume24h > 0 || volumeKlines > 0 {
		log.Printf("📊 Volume comparison for %s: 24h-based=$%.2f, klines=$%.2f → using $%.2f",
			ticker.Symbol, volume24h, volumeKlines, finalVolume)
	}

	return finalVolume
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

				// Рассчитываем объем двумя методами
				volumeUSD := calculateVolumeUSD(client, ticker, currentPrice, cfg.IntervalSeconds)

				log.Printf("📊 Volume check: %s $%.2f (required: $%.2f)",
					ticker.Symbol, volumeUSD, cfg.VolumeUSD)

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
