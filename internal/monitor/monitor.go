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

type PricePoint struct {
	Symbol string
	Price  float64
	Volume float64
	Time   time.Time
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

func Run(client *api.MEXCClient, cfg *config.Config, bl *blacklist.Blacklist, bot *telegram.Bot) {
	priceHistory := make(map[string][]PricePoint)

	for {
		startTime := time.Now()
		log.Printf("Starting ticker fetch cycle at %s", startTime)

		tickers, err := client.GetTickers()
		if err != nil {
			log.Printf("Error fetching tickers: %v", err)
			time.Sleep(5 * time.Second)
			continue
		}
		log.Printf("Fetched %d tickers", len(tickers))

		for _, ticker := range tickers {
			if bl.IsBlacklisted(ticker.Symbol) {
				log.Printf("Ticker %s is blacklisted, skipping", ticker.Symbol)
				continue
			}

			price, err := strconv.ParseFloat(ticker.Price, 64)
			if err != nil {
				log.Printf("Error parsing price for %s: %v", ticker.Symbol, err)
				continue
			}

			current := PricePoint{
				Symbol: ticker.Symbol,
				Price:  price,
				Time:   time.Now(),
			}

			history, exists := priceHistory[ticker.Symbol]
			if !exists {
				priceHistory[ticker.Symbol] = []PricePoint{current}
				continue
			}

			var priceChanged bool
			var priceChangePct float64
			for _, past := range history {
				timeDiff := current.Time.Sub(past.Time).Seconds()
				if timeDiff <= float64(cfg.IntervalSeconds) {
					if past.Price == 0 {
						log.Printf("Past price is zero for %s, skipping", ticker.Symbol)
						continue
					}
					priceChangePct = ((current.Price - past.Price) / past.Price) * 100
					if math.Abs(priceChangePct) >= cfg.PriceChangePct {
						priceChanged = true
						break
					}
				}
			}

			if priceChanged {
				log.Printf("Price changed for %s: %.2f%%", ticker.Symbol, priceChangePct)

				// Try multiple approaches to get volume data
				var volumeUSD float64
				var volumeSource string

				// Approach 1: Try to get klines data for accurate volume calculation
				// Use a longer period to ensure we get some data
				endTime := current.Time.Unix()
				startTime := endTime - int64(math.Max(float64(cfg.IntervalSeconds)*2, 300)) // At least 5 minutes

				klines, err := client.GetKline(ticker.Symbol, startTime, endTime)
				if err == nil && len(klines) > 0 {
					// Calculate volume from klines using QuoteAssetVolume (already in USDT)
					var totalVolumeUSD float64
					var validKlines int
					var recentKlines int

					// Focus on more recent klines but use all available data
					recentThreshold := endTime - int64(cfg.IntervalSeconds)

					for _, kline := range klines {
						isRecent := kline.Timestamp >= recentThreshold
						if isRecent {
							recentKlines++
						}

						if kline.QuoteAssetVolume > 0 {
							// QuoteAssetVolume is already in quote currency (USDT/USD)
							weight := 1.0
							if isRecent {
								weight = 2.0 // Give more weight to recent data
							}
							totalVolumeUSD += kline.QuoteAssetVolume * weight
							validKlines++
						} else if kline.Volume > 0 && kline.Close > 0 {
							// Fallback: use base volume * close price
							weight := 1.0
							if isRecent {
								weight = 2.0
							}
							volumeCalc := kline.Volume * kline.Close * weight
							totalVolumeUSD += volumeCalc
							validKlines++
						}
					}

					if totalVolumeUSD > 0 {
						// Normalize the volume based on the time period
						actualPeriod := float64(len(klines))
						expectedPeriod := float64(cfg.IntervalSeconds) / 60.0 // Convert to minutes
						if actualPeriod > expectedPeriod {
							totalVolumeUSD = totalVolumeUSD * (expectedPeriod / actualPeriod)
						}

						volumeUSD = totalVolumeUSD
						volumeSource = fmt.Sprintf("klines (%d/%d valid, %d recent)", validKlines, len(klines), recentKlines)
					}
					log.Printf("Klines debug for %s: %d total, %d recent, volume: $%.2f", ticker.Symbol, len(klines), recentKlines, totalVolumeUSD)
				} else {
					log.Printf("Klines failed for %s: %v", ticker.Symbol, err)
				}

				// Approach 2: If klines failed or returned 0, try using 24h volume estimate
				if volumeUSD == 0 {
					quoteVolume24h, quoteVolErr := strconv.ParseFloat(ticker.QuoteVol24h, 64)
					volume24h, volErr := strconv.ParseFloat(ticker.Volume24h, 64)

					if quoteVolErr == nil && quoteVolume24h > 0 {
						// Use quote volume (already in USD/USDT) - most accurate
						// Be more conservative with the estimate (divide by more than 24h worth of minutes)
						intervalMinutes := float64(cfg.IntervalSeconds) / 60.0
						minutesIn24h := 1440.0

						// Add volatility multiplier - more volatile coins likely have uneven volume distribution
						priceChangeMagnitude := math.Abs(priceChangePct)
						volatilityMultiplier := 1.0
						if priceChangeMagnitude >= 15 {
							volatilityMultiplier = 3.0 // High volatility = volume spikes
						} else if priceChangeMagnitude >= 10 {
							volatilityMultiplier = 2.0
						} else if priceChangeMagnitude >= 5 {
							volatilityMultiplier = 1.5
						}

						estimatedVolume := (quoteVolume24h / minutesIn24h) * intervalMinutes * volatilityMultiplier
						volumeUSD = estimatedVolume
						volumeSource = fmt.Sprintf("24h quote estimate (×%.1f volatility)", volatilityMultiplier)
					} else if volErr == nil && volume24h > 0 && current.Price > 0 {
						// Use base volume * current price
						intervalMinutes := float64(cfg.IntervalSeconds) / 60.0
						minutesIn24h := 1440.0

						priceChangeMagnitude := math.Abs(priceChangePct)
						volatilityMultiplier := 1.0
						if priceChangeMagnitude >= 15 {
							volatilityMultiplier = 3.0
						} else if priceChangeMagnitude >= 10 {
							volatilityMultiplier = 2.0
						} else if priceChangeMagnitude >= 5 {
							volatilityMultiplier = 1.5
						}

						estimatedVolume := ((volume24h * current.Price) / minutesIn24h) * intervalMinutes * volatilityMultiplier
						volumeUSD = estimatedVolume
						volumeSource = fmt.Sprintf("24h base estimate (×%.1f volatility)", volatilityMultiplier)
					}
				}

				// Approach 3: If still 0, use price change magnitude as volume indicator
				if volumeUSD == 0 {
					// Rough estimate based on price movement - not ideal but better than 0
					priceChangeMagnitude := math.Abs(priceChangePct)
					if priceChangeMagnitude >= 15 {
						volumeUSD = 8000 // Assume significant volume for large moves
						volumeSource = "estimated (large price movement)"
					} else if priceChangeMagnitude >= 10 {
						volumeUSD = 3000
						volumeSource = "estimated (medium price movement)"
					} else {
						volumeUSD = 1000
						volumeSource = "estimated (small price movement)"
					}
				}

				current.Volume = volumeUSD
				log.Printf("Volume for %s: $%.2f (%s)", ticker.Symbol, current.Volume, volumeSource)

				if current.Volume >= cfg.VolumeUSD {
					directionEmoji := "🟢"
					if priceChangePct < 0 {
						directionEmoji = "🔴"
					}
					circleEmojis := getCircleEmojis(priceChangePct)
					eyeEmoji := getEyeEmoji(current.Volume)
					fireEmojis := getFireEmojis(current.Volume)
					msg := fmt.Sprintf(
						"%s %s\n%.2f%% %s\n%d $ %s%s",
						strings.ToUpper(ticker.Symbol), directionEmoji,
						math.Abs(priceChangePct), circleEmojis,
						int(current.Volume), eyeEmoji, fireEmojis,
					)
					log.Printf("Sending alert for %s: %s", ticker.Symbol, msg)
					bot.SendMessage(msg)
					bl.Add(ticker.Symbol, 10*time.Minute)
				}
			}

			priceHistory[ticker.Symbol] = append(history, current)
		}

		// Clean up priceHistory
		for symbol, points := range priceHistory {
			var newPoints []PricePoint
			for _, point := range points {
				if time.Now().Sub(point.Time).Seconds() <= float64(cfg.IntervalSeconds) {
					newPoints = append(newPoints, point)
				}
			}
			if len(newPoints) == 0 {
				delete(priceHistory, symbol)
			} else {
				priceHistory[symbol] = newPoints
			}
		}

		totalPoints := 0
		for _, points := range priceHistory {
			totalPoints += len(points)
		}
		log.Printf("Total price history points: %d", totalPoints)

		elapsed := time.Since(startTime)
		intervalDuration := time.Duration(int64(cfg.IntervalSeconds)) * time.Second
		if elapsed < intervalDuration {
			time.Sleep(intervalDuration - elapsed)
		} else {
			log.Printf("Warning: Cycle took longer than interval (%v > %v)", elapsed, intervalDuration)
		}
	}
}
