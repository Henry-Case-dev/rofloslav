package bot

import (
	"fmt"
	"log"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/config"
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

		// Отправляем основное меню С ИНФОРМАЦИЕЙ О МОДЕЛИ
		modelInfo := fmt.Sprintf("Текущая модель: %s (%s)", b.config.LLMProvider, b.getCurrentModelName())
		b.sendReplyWithKeyboard(chatID, "Бот готов к работе!\n"+modelInfo, getMainKeyboard())
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
	case "toggle_srach_analysis": // Используем одно имя для переключения
		b.settingsMutex.Lock()
		if settings, exists := b.chatSettings[chatID]; exists {
			settings.SrachAnalysisEnabled = !settings.SrachAnalysisEnabled
			log.Printf("Чат %d: Анализ срачей переключен на %s", chatID, getEnabledStatusText(settings.SrachAnalysisEnabled))
			// Сбрасываем состояние срача при переключении
			settings.SrachState = "none"
			settings.SrachMessages = nil
			b.answerCallback(callback.ID, fmt.Sprintf("Анализ срачей: %s", getEnabledStatusText(settings.SrachAnalysisEnabled)))
		} else {
			b.answerCallback(callback.ID, "Ошибка: Настройки чата не найдены")
		}
		b.settingsMutex.Unlock()
		b.updateSettingsKeyboard(callback) // Обновляем клавиатуру
		return

	// --- Восстановленные/Добавленные обработчики кнопок настроек ---
	case "toggle_active": // Вкл/Выкл бота (оставляем логику на случай, если кнопка вернется, но из меню уберем)
		b.settingsMutex.Lock()
		if settings, exists := b.chatSettings[chatID]; exists {
			settings.Active = !settings.Active
			log.Printf("Чат %d: Активность бота переключена на %t", chatID, settings.Active)
			b.answerCallback(callback.ID, fmt.Sprintf("Бот теперь %s", map[bool]string{true: "активен", false: "неактивен"}[settings.Active]))
		} else {
			b.answerCallback(callback.ID, "Ошибка: Настройки чата не найдены")
		}
		b.settingsMutex.Unlock()
		b.updateSettingsKeyboard(callback) // Обновляем клавиатуру
		return

	case "change_interval":
		settingToSet = "min_messages" // Начинаем с запроса минимального значения
		promptText = b.config.PromptEnterMinMessages
		// Удаляем старое меню перед запросом
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, callback.Message.MessageID)
		b.api.Request(deleteMsg)

	case "change_daily_time":
		settingToSet = "daily_time"
		promptText = fmt.Sprintf(b.config.PromptEnterDailyTime, b.config.TimeZone) // Подставляем часовой пояс
		// Удаляем старое меню перед запросом
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, callback.Message.MessageID)
		b.api.Request(deleteMsg)

	case "change_summary_interval":
		settingToSet = "summary_interval"
		promptText = b.config.PromptEnterSummaryInterval
		// Удаляем старое меню перед запросом
		deleteMsg := tgbotapi.NewDeleteMessage(chatID, callback.Message.MessageID)
		b.api.Request(deleteMsg)

	default:
		log.Printf("Неизвестный callback data: %s от пользователя %d в чате %d", callback.Data, callback.From.ID, chatID)
		b.answerCallback(callback.ID, "Неизвестное действие") // Сообщаем пользователю
		// Не обновляем клавиатуру, так как действие неизвестно
		return // Выходим
	}

	// Если мы дошли сюда, значит, была нажата кнопка "change_..." для установки значения
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

func (b *Bot) getCurrentModelName() string {
	switch b.config.LLMProvider {
	case config.ProviderGemini:
		return b.config.GeminiModelName
	case config.ProviderDeepSeek:
		return b.config.DeepSeekModelName
	default:
		return "Неизвестная модель"
	}
}
