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

// PriceData хранит только нужные данные для каждой монеты
type PriceData struct {
	Symbol    string
	Price     float64
	Timestamp time.Time
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

// calculateVolumeUSD упрощенный расчет объема
func calculateVolumeUSD(ticker api.Ticker, currentPrice float64, intervalSeconds int) float64 {
	// Используем QuoteVolume24h (уже в USD/USDT) - самый точный показатель
	quoteVolume24h, err := strconv.ParseFloat(ticker.QuoteVol24h, 64)
	if err == nil && quoteVolume24h > 0 {
		// Простое пропорциональное распределение
		intervalMinutes := float64(intervalSeconds) / 60.0
		minutesIn24h := 1440.0

		// Базовый объем за интервал
		baseVolume := (quoteVolume24h / minutesIn24h) * intervalMinutes

		// Получаем процент изменения за 24 часа для корректировки
		changePct24h, changePctErr := strconv.ParseFloat(ticker.ChangePct, 64)

		// Корректируем на волатильность - если есть большие движения,
		// объем вероятно распределен неравномерно
		volatilityMultiplier := 1.0
		if changePctErr == nil {
			absChangePct := math.Abs(changePct24h)
			if absChangePct >= 20 {
				volatilityMultiplier = 4.0 // Очень высокая волатильность
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
		intervalMinutes := float64(intervalSeconds) / 60.0
		minutesIn24h := 1440.0

		baseVolume := ((volume24h * currentPrice) / minutesIn24h) * intervalMinutes

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

	// Если ничего не работает, возвращаем 0
	return 0
}

func Run(client *api.MEXCClient, cfg *config.Config, bl *blacklist.Blacklist, bot *telegram.Bot) {
	// Храним только базовые цены для сравнения (timestamp интервал назад -> цена)
	priceBaseline := make(map[string]PriceData)

	for {
		startTime := time.Now()
		log.Printf("Starting ticker fetch cycle at %s", startTime.Format("15:04:05"))

		tickers, err := client.GetTickers()
		if err != nil {
			log.Printf("Error fetching tickers: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		log.Printf("Fetched %d tickers", len(tickers))

		processedCount := 0
		alertCount := 0

		for _, ticker := range tickers {
			if bl.IsBlacklisted(ticker.Symbol) {
				continue
			}

			currentPrice, err := strconv.ParseFloat(ticker.Price, 64)
			if err != nil {
				log.Printf("Error parsing price for %s: %v", ticker.Symbol, err)
				continue
			}

			if currentPrice <= 0 {
				continue
			}

			processedCount++
			currentTime := time.Now()

			// Проверяем, есть ли базовая цена для сравнения
			baseline, exists := priceBaseline[ticker.Symbol]
			if !exists {
				// Первый раз видим эту монету - запоминаем текущую цену как базовую
				priceBaseline[ticker.Symbol] = PriceData{
					Symbol:    ticker.Symbol,
					Price:     currentPrice,
					Timestamp: currentTime,
				}
				continue
			}

			// Проверяем, прошел ли нужный интервал
			timeDiff := currentTime.Sub(baseline.Timestamp).Seconds()
			if timeDiff < float64(cfg.IntervalSeconds) {
				continue // Еще рано проверять эту монету
			}

			// Рассчитываем изменение цены
			priceChangePct := ((currentPrice - baseline.Price) / baseline.Price) * 100

			log.Printf("Price check for %s: %.8f -> %.8f (%.2f%% in %.0fs)",
				ticker.Symbol, baseline.Price, currentPrice, priceChangePct, timeDiff)

			// Проверяем, превышает ли изменение пороговое значение
			if math.Abs(priceChangePct) >= cfg.PriceChangePct {
				// Рассчитываем объем
				volumeUSD := calculateVolumeUSD(ticker, currentPrice, cfg.IntervalSeconds)

				log.Printf("Significant price change for %s: %.2f%%, volume: $%.2f",
					ticker.Symbol, priceChangePct, volumeUSD)

				// Проверяем объем
				if volumeUSD >= cfg.VolumeUSD {
					directionEmoji := "🟢"
					if priceChangePct < 0 {
						directionEmoji = "🔴"
					}

					circleEmojis := getCircleEmojis(priceChangePct)
					eyeEmoji := getEyeEmoji(volumeUSD)
					fireEmojis := getFireEmojis(volumeUSD)

					msg := fmt.Sprintf(
						"%s %s\n%.2f%% %s\n$%.0f %s%s",
						strings.ToUpper(ticker.Symbol), directionEmoji,
						math.Abs(priceChangePct), circleEmojis,
						volumeUSD, eyeEmoji, fireEmojis,
					)

					log.Printf("🚨 ALERT: %s", msg)
					bot.SendMessage(msg)
					bl.Add(ticker.Symbol, 10*time.Minute)
					alertCount++
				} else {
					log.Printf("Volume too low for %s: $%.2f < $%.2f",
						ticker.Symbol, volumeUSD, cfg.VolumeUSD)
				}
			}

			// Обновляем базовую цену для следующего сравнения
			priceBaseline[ticker.Symbol] = PriceData{
				Symbol:    ticker.Symbol,
				Price:     currentPrice,
				Timestamp: currentTime,
			}
		}

		// Очистка старых данных (старше 2 интервалов)
		cleanupThreshold := time.Now().Add(-time.Duration(cfg.IntervalSeconds*2) * time.Second)
		cleanedCount := 0
		for symbol, data := range priceBaseline {
			if data.Timestamp.Before(cleanupThreshold) {
				delete(priceBaseline, symbol)
				cleanedCount++
			}
		}

		elapsed := time.Since(startTime)
		log.Printf("Cycle complete: processed %d tickers, %d alerts, cleaned %d old entries in %v",
			processedCount, alertCount, cleanedCount, elapsed)

		// Ждем до следующего цикла
		intervalDuration := time.Duration(cfg.IntervalSeconds) * time.Second
		if elapsed < intervalDuration {
			sleepDuration := intervalDuration - elapsed
			log.Printf("Sleeping for %v until next cycle", sleepDuration)
			time.Sleep(sleepDuration)
		} else {
			log.Printf("⚠️ Warning: Cycle took longer than interval (%v > %v)", elapsed, intervalDuration)
		}
	}
}
