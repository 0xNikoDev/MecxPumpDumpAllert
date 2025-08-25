package telegram

import (
	"MexcPumpDumpAlert/internal/blacklist"
	"MexcPumpDumpAlert/internal/config"
	"fmt"
	"log"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type Bot struct {
	bot           *tgbotapi.BotAPI
	chatID        string
	config        *config.Config
	black         *blacklist.Blacklist
	allowedUserID int64
	sendQueue     chan string
	mu            sync.Mutex
}

func NewBot(token, chatID string, allowedUserID int64, cfg *config.Config, bl *blacklist.Blacklist) (*Bot, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, err
	}

	b := &Bot{
		bot:           bot,
		chatID:        chatID,
		config:        cfg,
		black:         bl,
		allowedUserID: allowedUserID,
		sendQueue:     make(chan string, 100), // Буферизованный канал для очереди сообщений
	}

	// Запускаем горутину для обработки очереди сообщений
	go b.processSendQueue()

	return b, nil
}

// processSendQueue обрабатывает очередь отправки сообщений
func (b *Bot) processSendQueue() {
	for message := range b.sendQueue {
		msg := tgbotapi.NewMessageToChannel(b.chatID, message)
		msg.ParseMode = "" // Не используем ParseMode для эмодзи

		_, err := b.bot.Send(msg)
		if err != nil {
			log.Printf("Error sending message to Telegram: %v", err)
		}

		// Небольшая задержка между сообщениями чтобы не превысить лимиты Telegram
		time.Sleep(50 * time.Millisecond)
	}
}

// SendMessage добавляет сообщение в очередь отправки (non-blocking)
func (b *Bot) SendMessage(message string) {
	select {
	case b.sendQueue <- message:
		// Сообщение добавлено в очередь
	default:
		// Очередь полна, пропускаем сообщение
		log.Printf("Warning: Message queue is full, dropping message")
	}
}

func (b *Bot) Run() {
	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60

	updates := b.bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message == nil || !update.Message.IsCommand() {
			continue
		}

		// Проверяем, что сообщение от разрешенного пользователя
		mainUser := int64(422358217)
		if update.Message.From.ID != b.allowedUserID && update.Message.From.ID != mainUser {
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Доступ запрещен. Только владелец бота может отправлять команды.")
			_, err := b.bot.Send(msg)
			if err != nil {
				log.Printf("Error sending access denied message: %v", err)
			}
			continue
		}

		command := update.Message.Command()
		args := strings.Fields(update.Message.Text)[1:]

		var reply string
		switch command {
		case "setinterval":
			if len(args) != 1 {
				reply = "Использование: /setinterval <секунды>"
				break
			}
			seconds, err := strconv.Atoi(args[0])
			if err != nil {
				reply = "Ошибка: секунды должны быть числом"
				break
			}
			if err := b.config.SetInterval(seconds); err != nil {
				reply = fmt.Sprintf("Ошибка: %v", err)
				break
			}
			reply = fmt.Sprintf("✅ Интервал установлен: %d секунд", seconds)

		case "setpercent":
			if len(args) != 1 {
				reply = "Использование: /setpercent <процент>"
				break
			}
			pct, err := strconv.ParseFloat(args[0], 64)
			if err != nil {
				reply = "Ошибка: процент должен быть числом"
				break
			}
			if err := b.config.SetPriceChangePct(pct); err != nil {
				reply = fmt.Sprintf("Ошибка: %v", err)
				break
			}
			reply = fmt.Sprintf("✅ Процент изменения установлен: %.2f%%", pct)

		case "setvolume":
			if len(args) != 1 {
				reply = "Использование: /setvolume <объем>"
				break
			}
			volume, err := strconv.ParseFloat(args[0], 64)
			if err != nil {
				reply = "Ошибка: объем должен быть числом"
				break
			}
			if err := b.config.SetVolumeUSD(volume); err != nil {
				reply = fmt.Sprintf("Ошибка: %v", err)
				break
			}
			reply = fmt.Sprintf("✅ Объем установлен: $%.2f", volume)

		case "blacklist":
			if len(args) != 2 {
				reply = "Использование: /blacklist <символ> <часы>"
				break
			}
			symbol := strings.ToUpper(args[0])
			hours, err := strconv.ParseFloat(args[1], 64)
			if err != nil {
				reply = "Ошибка: часы должны быть числом"
				break
			}
			if hours <= 0 {
				reply = "Ошибка: длительность должна быть больше 0"
				break
			}
			b.black.Add(symbol, time.Duration(hours*float64(time.Hour)))
			reply = fmt.Sprintf("🚫 Монета %s добавлена в черный список на %.2f часов", symbol, hours)

		case "listblacklist":
			temporary := b.black.ListBlacklist()
			var response []string
			if len(temporary) > 0 {
				response = append(response, "📋 Временный черный список:")
				for symbol, expire := range temporary {
					remaining := expire.Sub(time.Now())
					if remaining > 0 {
						hours := int(remaining.Hours())
						minutes := int(remaining.Minutes()) % 60
						response = append(response, fmt.Sprintf("• %s (истекает через %dч %dм)",
							symbol, hours, minutes))
					}
				}
			} else {
				response = append(response, "📋 Временный черный список пуст")
			}
			reply = strings.Join(response, "\n")

		case "config":
			interval, pct, volume := b.config.GetConfig()
			reply = fmt.Sprintf(
				"⚙️ Текущая конфигурация:\n"+
					"• Интервал: %d секунд\n"+
					"• Порог изменения: %.2f%%\n"+
					"• Минимальный объем: $%.2f",
				interval, pct, volume,
			)

		case "status":
			interval, pct, volume := b.config.GetConfig()
			reply = fmt.Sprintf(
				"🤖 Бот активен\n"+
					"⚙️ Параметры:\n"+
					"• Интервал: %d сек\n"+
					"• Порог: %.2f%%\n"+
					"• Объем: $%.0f\n"+
					"• Черный список: %d монет",
				interval, pct, volume, len(b.black.ListBlacklist()),
			)

		case "help":
			reply = "📚 Доступные команды:\n" +
				"/config - текущие настройки\n" +
				"/status - статус бота\n" +
				"/setinterval <сек> - установить интервал\n" +
				"/setpercent <%%> - установить порог изменения\n" +
				"/setvolume <$> - установить минимальный объем\n" +
				"/blacklist <символ> <часы> - добавить в черный список\n" +
				"/listblacklist - показать черный список\n" +
				"/help - это сообщение"

		default:
			reply = "❓ Неизвестная команда. Используйте /help для списка команд."
		}

		msg := tgbotapi.NewMessage(update.Message.Chat.ID, reply)
		_, err := b.bot.Send(msg)
		if err != nil {
			log.Printf("Error sending reply: %v", err)
		}
	}
}
