package telegram

import (
	"MexcPumpDumpAlert/internal/blacklist"
	"MexcPumpDumpAlert/internal/config"
	"fmt"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

type Bot struct {
	bot           *tgbotapi.BotAPI
	chatID        string
	config        *config.Config
	black         *blacklist.Blacklist
	allowedUserID int64
}

func NewBot(token, chatID string, allowedUserID int64, cfg *config.Config, bl *blacklist.Blacklist) (*Bot, error) {
	bot, err := tgbotapi.NewBotAPI(token)
	if err != nil {
		return nil, err
	}
	return &Bot{
		bot:           bot,
		chatID:        chatID,
		config:        cfg,
		black:         bl,
		allowedUserID: allowedUserID,
	}, nil
}

func (b *Bot) SendMessage(message string) {
	msg := tgbotapi.NewMessageToChannel(b.chatID, message)
	_, err := b.bot.Send(msg)
	if err != nil {
		// Логирование ошибки
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
		if update.Message.From.ID != b.allowedUserID {
			msg := tgbotapi.NewMessage(update.Message.Chat.ID, "Доступ запрещен. Только владелец бота может отправлять команды.")
			_, err := b.bot.Send(msg)
			if err != nil {
				// Логирование ошибки
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
			reply = fmt.Sprintf("Интервал установлен: %d секунд", seconds)

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
			reply = fmt.Sprintf("Процент изменения установлен: %.2f%%", pct)

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
			reply = fmt.Sprintf("Объем установлен: $%.2f", volume)

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
			reply = fmt.Sprintf("Монета %s добавлена в черный список на %.2f часов", symbol, hours)

		case "listblacklist":
			temporary := b.black.ListBlacklist()
			var response []string
			if len(temporary) > 0 {
				var tempList []string
				for symbol, expire := range temporary {
					remaining := expire.Sub(time.Now()).Hours()
					tempList = append(tempList, fmt.Sprintf("- %s (истекает через %.2f часов)", symbol, remaining))
				}
				response = append(response, fmt.Sprintf("Временный черный список:\n%s", strings.Join(tempList, "\n")))
			} else {
				response = append(response, "Временный черный список пуст")
			}
			reply = strings.Join(response, "\n\n")

		case "config":
			interval, pct, volume := b.config.GetConfig()
			reply = fmt.Sprintf(
				"Текущая конфигурация:\nИнтервал: %d секунд\nПроцент: %.2f%%\nОбъем: $%.2f",
				interval, pct, volume,
			)

		default:
			reply = "Неизвестная команда. Доступные команды: /setinterval, /setpercent, /setvolume, " +
				"/blacklist, /listblacklist, /config"
		}

		msg := tgbotapi.NewMessage(update.Message.Chat.ID, reply)
		_, err := b.bot.Send(msg)
		if err != nil {
			// Логирование ошибки
		}
	}
}
