package bot

import (
	"fmt"
	"log"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleCallback обрабатывает нажатия на кнопки
func (b *Bot) handleCallback(callback *tgbotapi.CallbackQuery) {
	chatID := callback.Message.Chat.ID

	// Общий ключ для PendingSetting (пока не используется, т.к. PendingSetting хранится для chatID)
	// pendingKey := fmt.Sprintf("%d", chatID)

	var promptText string
	var settingToSet string

	switch callback.Data {
	case "set_min_messages":
		settingToSet = "min_messages"
		promptText = b.config.PromptEnterMinMessages
	case "set_max_messages":
		settingToSet = "max_messages"
		promptText = b.config.PromptEnterMaxMessages
	case "set_daily_time":
		settingToSet = "daily_time"
		promptText = fmt.Sprintf(b.config.PromptEnterDailyTime, b.config.TimeZone) // Подставляем часовой пояс в промпт
	case "set_summary_interval":
		settingToSet = "summary_interval"
		promptText = b.config.PromptEnterSummaryInterval
	case "back_to_main":
		b.settingsMutex.Lock()
		// Сбрасываем ожидание ввода при выходе из настроек
		if settings, exists := b.chatSettings[chatID]; exists {
			settings.PendingSetting = ""
			deleteMsg := tgbotapi.NewDeleteMessage(chatID, callback.Message.MessageID) // Удаляем само меню настроек
			b.api.Request(deleteMsg)
		}
		b.settingsMutex.Unlock()

		// Отправляем основное меню
		b.sendReplyWithKeyboard(chatID, "Бот готов к работе!", getMainKeyboard())
		b.answerCallback(callback.ID, "") // Отвечаем на колбэк
		return                            // Выходим, дальнейшая обработка не нужна

	case "summary": // Обработка кнопки саммари из основного меню
		b.answerCallback(callback.ID, "Запрашиваю саммари...")
		// Корректно имитируем сообщение с командой
		// Создаем базовое сообщение, похожее на то, что пришло бы от пользователя
		fakeMessage := &tgbotapi.Message{
			MessageID: callback.Message.MessageID, // Можно использовать ID кнопки для контекста, но не обязательно
			From:      callback.From,              // Кто нажал кнопку
			Chat:      callback.Message.Chat,      // В каком чате
			Date:      int(time.Now().Unix()),     // Текущее время
			Text:      "/summary",                 // Текст команды
			Entities: []tgbotapi.MessageEntity{ // Указываем, что это команда
				{Type: "bot_command", Offset: 0, Length: len("/summary")},
			},
		}
		b.handleCommand(fakeMessage) // Передаем имитированное сообщение
		return                       // Выходим

	case "settings": // Обработка кнопки настроек из основного меню
		// Удаляем сообщение с основным меню
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, callback.Message.MessageID)
		b.api.Request(deleteMsg)
		b.sendSettingsKeyboard(chatID)
		b.answerCallback(callback.ID, "") // Отвечаем на колбэк
		return                            // Выходим

	case "stop": // Обработка кнопки паузы из основного меню
		b.settingsMutex.Lock()
		if settings, exists := b.chatSettings[chatID]; exists {
			settings.Active = false
		}
		b.settingsMutex.Unlock()
		// Удаляем сообщение с основным меню
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, callback.Message.MessageID)
		b.api.Request(deleteMsg)
		// Отправляем текстовое подтверждение
		b.sendReply(chatID, "Бот поставлен на паузу. Используйте /start чтобы возобновить.")
		b.answerCallback(callback.ID, "Бот остановлен")
		return // Выходим

	// Новые коллбэки для управления анализом срачей
	case "toggle_srach_on":
		b.setSrachAnalysis(chatID, true)
		b.answerCallback(callback.ID, "🔥 Анализ срачей включен")
		b.updateSettingsKeyboard(callback) // Обновляем сообщение с клавиатурой
		return                             // Выходим, дальнейшая обработка не нужна
	case "toggle_srach_off":
		b.setSrachAnalysis(chatID, false)
		b.answerCallback(callback.ID, "💀 Анализ срачей выключен")
		b.updateSettingsKeyboard(callback) // Обновляем сообщение с клавиатурой
		return                             // Выходим, дальнейшая обработка не нужна

	default:
		log.Printf("Неизвестный callback data: %s", callback.Data)
		b.answerCallback(callback.ID, "Неизвестное действие")
		return // Выходим
	}

	// Если мы дошли сюда, значит, была нажата кнопка "Установить..."
	if settingToSet != "" {
		b.settingsMutex.Lock()
		if settings, exists := b.chatSettings[chatID]; exists {
			settings.PendingSetting = settingToSet // Устанавливаем ожидание
		}
		b.settingsMutex.Unlock()

		// Отправляем сообщение с запросом ввода
		// Сначала удаляем старое меню настроек
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, callback.Message.MessageID)
		b.api.Request(deleteMsg)
		// Затем отправляем промпт
		b.sendReply(chatID, promptText+"\n\nИли отправьте /cancel для отмены.")
		b.answerCallback(callback.ID, "Ожидаю ввода...")
	}
}
