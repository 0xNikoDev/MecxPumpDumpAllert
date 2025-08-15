package main

import (
	"MexcPumpDumpAlert/internal/api"
	"MexcPumpDumpAlert/internal/blacklist"
	"MexcPumpDumpAlert/internal/config"
	"MexcPumpDumpAlert/internal/monitor"
	"MexcPumpDumpAlert/internal/telegram"
	"log"
	"os"
	"runtime/debug"
	"strconv"
	"time"

	"github.com/joho/godotenv"
)

func main() {
	log.Println("🚀 Starting MEXC Pump/Dump Alert Bot...")

	// Загружаем .env файл
	err := godotenv.Load()
	if err != nil {
		log.Printf("⚠️  Не удалось загрузить .env файл: %v. Используются переменные окружения.", err)
	}

	// Читаем конфигурацию из переменных окружения
	telegramToken := os.Getenv("TELEGRAM_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")
	allowedUserIDStr := os.Getenv("TELEGRAM_ALLOWED_USER_ID")
	intervalSecondsStr := os.Getenv("INTERVAL_SECONDS")
	priceChangePctStr := os.Getenv("PRICE_CHANGE_PCT")
	volumeUSDStr := os.Getenv("VOLUME_USD")

	// Проверяем обязательные переменные
	if telegramToken == "" || chatID == "" || allowedUserIDStr == "" {
		log.Fatal("❌ TELEGRAM_TOKEN, TELEGRAM_CHAT_ID и TELEGRAM_ALLOWED_USER_ID должны быть заданы")
	}

	// Парсим числовые параметры
	allowedUserID, err := strconv.ParseInt(allowedUserIDStr, 10, 64)
	if err != nil {
		log.Fatalf("❌ Ошибка парсинга TELEGRAM_ALLOWED_USER_ID: %v", err)
	}

	intervalSeconds, err := strconv.Atoi(intervalSecondsStr)
	if err != nil || intervalSeconds <= 0 {
		intervalSeconds = 60 // default value
		log.Printf("⚠️  Используется значение по умолчанию для INTERVAL_SECONDS: %d", intervalSeconds)
	}

	priceChangePct, err := strconv.ParseFloat(priceChangePctStr, 64)
	if err != nil || priceChangePct <= 0 {
		priceChangePct = 10.0 // default value
		log.Printf("⚠️  Используется значение по умолчанию для PRICE_CHANGE_PCT: %.1f%%", priceChangePct)
	}

	volumeUSD, err := strconv.ParseFloat(volumeUSDStr, 64)
	if err != nil || volumeUSD <= 0 {
		volumeUSD = 50000 // default value
		log.Printf("⚠️  Используется значение по умолчанию для VOLUME_USD: $%.0f", volumeUSD)
	}

	// Создаем конфигурацию
	cfg := config.NewConfig(intervalSeconds, priceChangePct, volumeUSD)
	bl := blacklist.NewBlacklist()

	log.Printf("📋 Конфигурация:")
	log.Printf("   - Интервал мониторинга: %d секунд", intervalSeconds)
	log.Printf("   - Минимальное изменение цены: %.1f%%", priceChangePct)
	log.Printf("   - Минимальный объем: $%.0f", volumeUSD)
	log.Printf("   - Telegram чат: %s", chatID)

	// Инициализируем Telegram бота
	bot, err := telegram.NewBot(telegramToken, chatID, allowedUserID, cfg, bl)
	if err != nil {
		log.Fatalf("❌ Ошибка инициализации Telegram бота: %v", err)
	}
	log.Println("✅ Telegram бот инициализирован")

	// Инициализируем MEXC API клиент
	mexcClient := api.NewMEXCClient()
	log.Println("✅ MEXC API клиент инициализирован")

	// Запускаем bot.Run в отдельной горутине
	go func() {
		for {
			func() {
				defer func() {
					if r := recover(); r != nil {
						log.Printf("💥 Panic in bot.Run: %v\nStack: %s", r, debug.Stack())
					}
				}()
				log.Println("🤖 Запуск Telegram бота...")
				bot.Run()
				log.Println("⚠️  Telegram бот остановлен. Перезапуск через 5 секунд...")
			}()
			time.Sleep(5 * time.Second)
		}
	}()

	log.Println("🔍 Начинаем мониторинг MEXC...")

	// Запускаем monitor.Run в основном потоке
	for {
		func() {
			defer func() {
				if r := recover(); r != nil {
					log.Printf("💥 Panic in monitor.Run: %v\nStack: %s", r, debug.Stack())
				}
			}()
			monitor.Run(mexcClient, cfg, bl, bot)
			log.Println("⚠️  Мониторинг остановлен. Перезапуск через 5 секунд...")
		}()
		time.Sleep(5 * time.Second)
	}
}
