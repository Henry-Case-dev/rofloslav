package bot

import (
	"fmt"
	"log"
	"strings"
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
		// и получаем ID сообщения с настройками для удаления
		var lastSettingsMsgID int
		if settings, exists := b.chatSettings[chatID]; exists {
			settings.PendingSetting = ""
			lastSettingsMsgID = settings.LastSettingsMessageID // Используем сохраненный ID
			// Удаляем само сообщение с настройками, если ID совпадает с callback.Message.MessageID
			// (на случай, если lastSettingsMsgID не был сохранен правильно)
			if lastSettingsMsgID == 0 || lastSettingsMsgID != callback.Message.MessageID {
				log.Printf("[WARN] LastSettingsMessageID (%d) не совпадает с callback.Message.MessageID (%d) для чата %d. Использую ID из колбэка.",
					lastSettingsMsgID, callback.Message.MessageID, chatID)
				lastSettingsMsgID = callback.Message.MessageID // Используем ID из колбэка как запасной вариант
			}
		} else {
			// Если настроек нет, все равно пытаемся удалить сообщение из колбэка
			lastSettingsMsgID = callback.Message.MessageID
		}
		b.settingsMutex.Unlock()

		// Отправляем основное меню, передавая ID старого меню настроек для удаления
		b.sendMainMenu(chatID, lastSettingsMsgID) // ID нового меню сохранится внутри sendMainMenu
		b.answerCallback(callback.ID, "")         // Отвечаем на колбэк
		return                                    // Выходим, дальнейшая обработка не нужна

	case "summary": // Обработка кнопки саммари из основного меню
		b.handleSummaryCommand(chatID, callback.Message.MessageID)
		b.answerCallback(callback.ID, "⏳ Запрос саммари отправлен...")
		return

	case "settings": // Обработка кнопки настроек из основного меню
		// Удаляем сообщение с основным меню (его ID берем из callback)
		lastMainMenuMsgID := callback.Message.MessageID
		// Отправляем клавиатуру настроек, передавая ID старого главного меню
		b.sendSettingsKeyboard(chatID, lastMainMenuMsgID) // ID нового меню сохранится внутри sendSettingsKeyboard
		b.answerCallback(callback.ID, "⚙️ Открываю настройки...")
		return

	case "stop": // Обработка кнопки паузы из основного меню
		b.settingsMutex.Lock()
		if settings, exists := b.chatSettings[chatID]; exists {
			settings.Active = false
			// Сохраняем ID меню для удаления
			lastMainMenuMsgID := callback.Message.MessageID
			settings.LastMenuMessageID = 0 // Сбрасываем сохраненный ID
			b.settingsMutex.Unlock()

			// Удаляем сообщение с основным меню
			deleteMsg := tgbotapi.NewDeleteMessage(chatID, lastMainMenuMsgID)
			b.api.Request(deleteMsg)

			// Отправляем текстовое подтверждение
			b.sendReply(chatID, "Бот поставлен на паузу. Используйте /start чтобы возобновить.")
			b.answerCallback(callback.ID, "Бот остановлен")
		} else {
			b.settingsMutex.Unlock()
			log.Printf("[WARN][Callback stop] Настройки для чата %d не найдены", chatID)
			b.answerCallback(callback.ID, "Ошибка остановки")
		}
		// Не обновляем клавиатуру, так как меню удалено
		return

	// Новые коллбэки для управления анализом срачей
	case "toggle_srach_analysis":
		log.Printf("[DEBUG][Callback] Chat %d: Получен коллбэк toggle_srach_analysis", chatID)
		newEnabled, err := b.toggleSrachAnalysis(chatID) // Вызываем обновленную функцию
		if err != nil {
			log.Printf("[ERROR][Callback] Chat %d: Ошибка toggleSrachAnalysis: %v", chatID, err)
			b.answerCallback(callback.ID, "Ошибка обновления настройки")
		} else {
			b.answerCallback(callback.ID, fmt.Sprintf("🤬 Анализ срачей: %s", getEnabledStatusText(newEnabled)))
			b.updateSettingsKeyboard(callback) // Обновляем клавиатуру
		}
		return

	// Переключение транскрипции голоса
	case "toggle_voice_transcription":
		log.Printf("[DEBUG][Callback] Chat %d: Получен коллбэк toggle_voice_transcription", chatID)
		// 1. Получаем текущие настройки из хранилища
		dbSettings, err := b.storage.GetChatSettings(chatID)
		if err != nil {
			log.Printf("[ERROR][Callback] Chat %d: Ошибка получения настроек из DB для toggle_voice_transcription: %v", chatID, err)
			b.answerCallback(callback.ID, "Ошибка получения настроек")
			return
		}

		// 2. Определяем текущее состояние (учитывая nil и дефолт)
		currentState := b.config.VoiceTranscriptionEnabledDefault // Значение по умолчанию
		if dbSettings.VoiceTranscriptionEnabled != nil {          // Если значение не nil, используем его
			currentState = *dbSettings.VoiceTranscriptionEnabled
		}
		log.Printf("[DEBUG][Callback] Chat %d: Текущее состояние VoiceTranscriptionEnabled: %t", chatID, currentState)

		// 3. Переключаем состояние
		newState := !currentState
		// dbSettings.VoiceTranscriptionEnabled = &newState // Обновляем указатель в настройках
		log.Printf("[DEBUG][Callback] Chat %d: Новое состояние VoiceTranscriptionEnabled: %t", chatID, newState)

		// 4. Сохраняем обновленные настройки используя метод хранилища
		err = b.storage.UpdateVoiceTranscriptionEnabled(chatID, newState)
		// err = b.storage.SetChatSettings(dbSettings) // Заменяем на прямой вызов
		if err != nil {
			log.Printf("[ERROR][Callback] Chat %d: Ошибка сохранения настройки VoiceTranscriptionEnabled в DB: %v", chatID, err)
			b.answerCallback(callback.ID, "Ошибка сохранения настройки")
			return
		}

		// 5. Отвечаем и обновляем клавиатуру
		statusText := getEnabledStatusText(newState)
		b.answerCallback(callback.ID, fmt.Sprintf("🎤 Распознавание голоса: %s", statusText))
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
		// Удаляем старое меню настроек перед запросом
		b.deleteMessage(chatID, callback.Message.MessageID)

	case "change_daily_time":
		settingToSet = "daily_time"
		promptText = fmt.Sprintf(b.config.PromptEnterDailyTime, b.config.TimeZone) // Подставляем часовой пояс
		// Удаляем старое меню настроек перед запросом
		b.deleteMessage(chatID, callback.Message.MessageID)

	case "change_summary_interval":
		settingToSet = "summary_interval"
		promptText = b.config.PromptEnterSummaryInterval
		// Удаляем старое меню настроек перед запросом
		b.deleteMessage(chatID, callback.Message.MessageID)

	case "toggle_direct_limit":
		log.Printf("[DEBUG][Callback] Chat %d: Получен коллбэк toggle_direct_limit", chatID)
		dbSettings, err := b.storage.GetChatSettings(chatID)
		if err != nil {
			log.Printf("[ERROR][Callback] Chat %d: Ошибка получения настроек из DB для toggle_direct_limit: %v", chatID, err)
			b.answerCallback(callback.ID, "Ошибка получения настроек")
			return
		}
		currentState := b.config.DirectReplyLimitEnabledDefault
		if dbSettings.DirectReplyLimitEnabled != nil {
			currentState = *dbSettings.DirectReplyLimitEnabled
		}
		newState := !currentState
		// dbSettings.DirectReplyLimitEnabled = &newState // Не меняем локальный объект
		// Используем специализированный метод обновления
		err = b.storage.UpdateDirectLimitEnabled(chatID, newState)
		// err = b.storage.SetChatSettings(dbSettings) // УДАЛЕНО
		if err != nil {
			log.Printf("[ERROR][Callback] Chat %d: Ошибка сохранения настройки DirectLimitEnabled в DB: %v", chatID, err)
			b.answerCallback(callback.ID, "Ошибка сохранения настройки")
			return
		}
		statusText := getEnabledStatusText(newState)
		b.answerCallback(callback.ID, fmt.Sprintf("Лимит прямых обращений: %s", statusText))
		b.updateSettingsKeyboard(callback)
		return

	case "change_direct_limit_values":
		log.Printf("[DEBUG][Callback] Chat %d: Получен коллбэк change_direct_limit_values", chatID)
		settingToSet = "direct_limit_count" // Начинаем с запроса количества
		promptText = b.config.PromptEnterDirectLimitCount
		// Удаляем старое меню настроек перед запросом
		b.deleteMessage(chatID, callback.Message.MessageID)
		// Остальная логика обработки запроса ввода остается ниже

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
		} else {
			// Если настроек нет, то и сохранять PendingSetting некуда
			log.Printf("[WARN] Настройки для чата %d не найдены при попытке установить PendingSetting.", chatID)
			b.settingsMutex.Unlock()
			b.answerCallback(callback.ID, "Ошибка: не удалось найти настройки чата.")
			return // Прерываем, так как не можем продолжить
		}
		b.settingsMutex.Unlock() // Разблокируем перед отправкой

		// Отправляем сообщение с запросом ввода
		// Старое меню настроек уже удалено выше по коду для change_*
		promptMsg := tgbotapi.NewMessage(chatID, promptText+"\n\nИли отправьте /cancel для отмены.")
		sentMsg, err := b.api.Send(promptMsg)
		if err != nil {
			log.Printf("Ошибка отправки промпта для ввода настройки '%s' в чат %d: %v", settingToSet, chatID, err)
			b.answerCallback(callback.ID, "Ошибка при запросе ввода.")
			// Сбрасываем PendingSetting, так как не смогли запросить ввод
			b.settingsMutex.Lock()
			if settings, exists := b.chatSettings[chatID]; exists {
				settings.PendingSetting = ""
			}
			b.settingsMutex.Unlock()
		} else {
			// Сохраняем ID отправленного информационного сообщения
			b.settingsMutex.Lock()
			if settings, exists := b.chatSettings[chatID]; exists {
				settings.LastInfoMessageID = sentMsg.MessageID
				if b.config.Debug {
					log.Printf("[DEBUG] Сохранен LastInfoMessageID: %d для чата %d (запрос '%s')", sentMsg.MessageID, chatID, settingToSet)
				}
			} else {
				log.Printf("[WARN] Настройки для чата %d не найдены при попытке сохранить LastInfoMessageID.", chatID)
			}
			b.settingsMutex.Unlock()
			b.answerCallback(callback.ID, "Ожидаю ввода...")
		}
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

// handleSummaryCommand - логика для команды /summary (вынесена из command_handler)
func (b *Bot) handleSummaryCommand(chatID int64, lastInfoMsgID int) {
	now := time.Now()
	b.summaryMutex.Lock() // Lock для lastSummaryRequest
	lastReq, ok := b.lastSummaryRequest[chatID]
	durationSinceLast := now.Sub(lastReq)
	if ok && durationSinceLast < summaryRequestInterval {
		remainingTime := summaryRequestInterval - durationSinceLast
		log.Printf("[DEBUG] Чат %d: /summary отклонен из-за rate limit. Прошло: %v < %v. Осталось: %v", chatID, durationSinceLast, summaryRequestInterval, remainingTime)
		b.summaryMutex.Unlock()

		// Генерация динамической части сообщения...
		dynamicPart := ""
		if b.config.RateLimitPrompt != "" {
			generatedText, err := b.llm.GenerateArbitraryResponse(b.config.RateLimitPrompt, "")
			if err != nil {
				log.Printf("[ERROR] Чат %d: Ошибка генерации динамической части сообщения о лимите: %v", chatID, err)
			} else {
				dynamicPart = strings.TrimSpace(generatedText)
			}
		}

		// Формирование и отправка итогового сообщения...
		fullMessage := fmt.Sprintf("%s %s\nПодожди еще: %s",
			b.config.RateLimitStaticText,
			dynamicPart,
			formatRemainingTime(remainingTime),
		)

		if lastInfoMsgID != 0 {
			b.deleteMessage(chatID, lastInfoMsgID)
		}
		msg := tgbotapi.NewMessage(chatID, fullMessage)
		sentMsg, err := b.api.Send(msg)
		if err == nil {
			b.settingsMutex.Lock()
			if set, ok := b.chatSettings[chatID]; ok {
				set.LastInfoMessageID = sentMsg.MessageID
			}
			b.settingsMutex.Unlock()
		}
		return
	}
	// Update last request time
	b.lastSummaryRequest[chatID] = now
	b.summaryMutex.Unlock()

	log.Printf("[DEBUG] Чат %d: /summary вызван. Последний запрос был: %v (ok=%t). Прошло: %v. Лимит: %v.",
		chatID, lastReq, ok, durationSinceLast, summaryRequestInterval)

	// Удаляем предыдущее инфо-сообщение...
	if lastInfoMsgID != 0 {
		b.deleteMessage(chatID, lastInfoMsgID)
	}
	// Отправляем сообщение о начале генерации и сохраняем его ID...
	msg := tgbotapi.NewMessage(chatID, "Генерирую саммари, подождите...")
	sentMsg, err := b.api.Send(msg)
	if err == nil {
		b.settingsMutex.Lock()
		if set, ok := b.chatSettings[chatID]; ok {
			set.LastInfoMessageID = sentMsg.MessageID
		}
		b.settingsMutex.Unlock()
	} else {
		log.Printf("[ERROR] Ошибка отправки сообщения 'Генерирую саммари...' в чат %d: %v", chatID, err)
	}

	// Запускаем генерацию в горутине
	go b.createAndSendSummary(chatID)
}
