package bot

import (
	"fmt"
	"log"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleCommand обрабатывает команды
func (b *Bot) handleCommand(message *tgbotapi.Message) {
	command := message.Command()
	chatID := message.Chat.ID

	switch command {
	case "start":
		b.settingsMutex.Lock()
		if settings, exists := b.chatSettings[chatID]; exists {
			settings.Active = true
		}
		b.settingsMutex.Unlock()

		b.sendReplyWithKeyboard(chatID, "Бот запущен и готов к работе!", getMainKeyboard())

	case "stop":
		b.settingsMutex.Lock()
		if settings, exists := b.chatSettings[chatID]; exists {
			settings.Active = false
		}
		b.settingsMutex.Unlock()

		b.sendReply(chatID, "Бот поставлен на паузу. Используйте /start чтобы возобновить.")

	case "summary":
		// Проверяем ограничение по времени
		b.summaryMutex.RLock()
		lastRequestTime, ok := b.lastSummaryRequest[chatID]
		b.summaryMutex.RUnlock()

		// Ограничение в 10 минут
		const rateLimitDuration = 10 * time.Minute
		timeSinceLastRequest := time.Since(lastRequestTime)

		if b.config.Debug {
			log.Printf("[DEBUG] Чат %d: /summary вызван. Последний запрос был: %v (ok=%t). Прошло: %s. Лимит: %s.",
				chatID, lastRequestTime, ok, timeSinceLastRequest.Round(time.Second), rateLimitDuration)
			log.Printf("[DEBUG] Чат %d: Сравниваем %s < %s ?", chatID, timeSinceLastRequest.Round(time.Second), rateLimitDuration)
			log.Printf("[DEBUG] Чат %d: Сообщение об ошибке из конфига: '%s'", chatID, b.config.RateLimitErrorMessage)
		}

		if ok && timeSinceLastRequest < rateLimitDuration {
			remainingTime := rateLimitDuration - timeSinceLastRequest
			// Формируем сообщение
			errorMsgText := b.config.RateLimitErrorMessage // Получаем текст из конфига
			fullErrorMsg := fmt.Sprintf("%s Осталось подождать: %s.",
				errorMsgText, // Используем полученный текст
				remainingTime.Round(time.Second).String(),
			)
			if b.config.Debug {
				log.Printf("[DEBUG] Чат %d: Rate limit активен. Текст ошибки из конфига: '%s'. Формированное сообщение: '%s'", chatID, errorMsgText, fullErrorMsg)
			}
			// Отправляем сформированное сообщение
			b.sendReply(chatID, fullErrorMsg)
			return
		}

		// Если ограничение прошло или запроса еще не было, обновляем время и генерируем саммари
		if b.config.Debug {
			log.Printf("[DEBUG] Чат %d: Rate limit пройден. Обновляю время последнего запроса на %v.", chatID, time.Now())
		}
		b.summaryMutex.Lock()
		b.lastSummaryRequest[chatID] = time.Now()
		b.summaryMutex.Unlock()

		if b.config.Debug {
			log.Printf("[DEBUG] Чат %d: Начинаю генерацию саммари (после обновления времени).", chatID)
		}
		go b.generateSummary(chatID) // Запускаем в горутине, чтобы не блокировать

	case "settings":
		b.sendSettingsKeyboard(chatID)

	case "menu": // Добавляем обработку /menu
		// Добавляем информацию о модели
		modelInfo := fmt.Sprintf("Текущая модель: %s (%s)", b.config.LLMProvider, b.getCurrentModelName())
		b.sendReplyWithKeyboard(chatID, "Главное меню:\n"+modelInfo, getMainKeyboard())

	case "srach": // Добавляем обработку /srach
		b.toggleSrachAnalysis(chatID)
		b.sendSettingsKeyboard(chatID) // Показываем обновленное меню настроек

		// Можно добавить default для неизвестных команд
		// default:
		// 	b.sendReply(chatID, "Неизвестная команда.")
	}
}
