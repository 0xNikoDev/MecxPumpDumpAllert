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
				
				// Get klines data for the period when price changed to calculate actual volume
				endTime := current.Time.Unix()
				startTime := endTime - int64(cfg.IntervalSeconds)
				
				klines, err := client.GetKline(ticker.Symbol, startTime, endTime)
				if err != nil {
					log.Printf("Error fetching klines for %s: %v", ticker.Symbol, err)
					// Fallback: try to estimate volume using 24h data
					volume24h, volumeErr := strconv.ParseFloat(ticker.Volume24h, 64)
					if volumeErr == nil {
						// Rough estimate: (24h volume / 1440 minutes) * interval minutes
						estimatedVolume := (volume24h * current.Price) * (float64(cfg.IntervalSeconds) / 60.0) / 1440.0
						current.Volume = estimatedVolume
						log.Printf("Using estimated volume for %s: $%.2f", ticker.Symbol, current.Volume)
					} else {
						log.Printf("Cannot estimate volume for %s, skipping", ticker.Symbol)
						continue
					}
				} else {
					// Calculate total volume in USD for the period
					var totalVolumeUSD float64
					for _, kline := range klines {
						// Use close price for volume calculation (more accurate than average)
						// Volume in base asset * close price = volume in USD
						volumeUSD := kline.Volume * kline.Close
						totalVolumeUSD += volumeUSD
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
