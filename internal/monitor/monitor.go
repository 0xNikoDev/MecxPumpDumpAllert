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
	if volume >= 10000 && volume < 50000 {
		return "👁️"
	}
	return ""
}

// getFireEmojis returns fire emojis based on volume
func getFireEmojis(volume float64) string {
	if volume < 50000 {
		return ""
	}
	fires := 1 // 50k–99k
	if volume >= 100000 {
		fires = 2 // 100k–199k
	}
	if volume >= 200000 {
		fires = 3 // 200k–299k
	}
	if volume >= 300000 {
		fires = 4 // 300k–399k
	}
	if volume >= 500000 {
		fires = int(math.Min(4+math.Floor((volume-500000)/100000), 10)) // +1 per 100k, max 10
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

				// Get klines data for the period when price changed to calculate actual volume
				endTime := current.Time.Unix()
				startTime := endTime - int64(cfg.IntervalSeconds)

				klines, err := client.GetKline(ticker.Symbol, startTime, endTime)
				if err != nil {
					log.Printf("Error fetching klines for %s: %v", ticker.Symbol, err)
					// Fallback: try to estimate volume using 24h data
					quoteVolume24h, volumeErr := strconv.ParseFloat(ticker.QuoteVol24h, 64)
					if volumeErr == nil && quoteVolume24h > 0 {
						// Estimate volume for interval: (24h quote volume / 1440 minutes) * interval minutes
						intervalMinutes := float64(cfg.IntervalSeconds) / 60.0
						estimatedVolume := (quoteVolume24h / 1440.0) * intervalMinutes
						current.Volume = estimatedVolume
						log.Printf("Using estimated volume for %s: $%.2f (based on 24h quote volume)", ticker.Symbol, current.Volume)
					} else {
						log.Printf("Cannot estimate volume for %s, skipping", ticker.Symbol)
						continue
					}
				} else {
					// Calculate total volume in USD for the period using QuoteAssetVolume
					var totalVolumeUSD float64
					for _, kline := range klines {
						// QuoteAssetVolume уже в USD/USDT - именно то что нам нужно!
						totalVolumeUSD += kline.QuoteAssetVolume
					}

					current.Volume = totalVolumeUSD
					log.Printf("Volume for %s during price change period: $%.2f (from %d klines)", ticker.Symbol, current.Volume, len(klines))
				}

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
