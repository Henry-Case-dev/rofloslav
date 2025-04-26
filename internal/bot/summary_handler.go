package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// === Вспомогательные функции для саммари ===

// escapeMarkdownV2 экранирует символы для MarkdownV2
// Взято из: https://core.telegram.org/bots/api#markdownv2-style
func escapeMarkdownV2(text string) string {
	// Список символов для экранирования по спецификации Telegram Bot API
	// '_', '*', '[', ']', '(', ')', '~', '`', '>', '#', '+', '-', '=', '|', '{', '}', '.', '!'
	var escaped strings.Builder
	for _, r := range text {
		switch r {
		case '_', '*', '[', ']', '(', ')', '~', '`', '>', '#', '+', '-', '=', '|', '{', '}', '.', '!':
			escaped.WriteRune('\\')
			escaped.WriteRune(r)
		default:
			escaped.WriteRune(r)
		}
	}
	return escaped.String()
}

// createAndSendSummary генерирует и отправляет/редактирует саммари
func (b *Bot) createAndSendSummary(chatID int64) {
	// --- Получаем настройки и ID последнего инфо-сообщения ---
	b.settingsMutex.RLock()
	chatSettings, exists := b.chatSettings[chatID]
	if !exists {
		b.settingsMutex.RUnlock()
		log.Printf("[ERROR][createAndSendSummary] Чат %d: Настройки не найдены.", chatID)
		return
	}
	// Копируем ID, чтобы разблокировать мьютекс как можно раньше
	lastInfoMsgID := chatSettings.LastInfoMessageID
	b.settingsMutex.RUnlock()

	if b.config.Debug {
		log.Printf("[DEBUG][createAndSendSummary] Запуск для чата %d. LastInfoMsgID: %d", chatID, lastInfoMsgID)
	}

	// --- Подготовка к редактированию/отправке сообщения ---
	var editText, sendText string // Тексты для редактирования и отправки
	var parseMode string = ""     // ParseMode для отправки/редактирования

	// 2. Получаем сообщения за последние 24 часа (или с момента последнего авто-саммари, если есть)
	sinceTime := time.Now().Add(-24 * time.Hour)
	b.settingsMutex.RLock()
	if settings, exists := b.chatSettings[chatID]; exists {
		if !settings.LastAutoSummaryTime.IsZero() && settings.LastAutoSummaryTime.After(sinceTime) {
			sinceTime = settings.LastAutoSummaryTime
		}
	}
	b.settingsMutex.RUnlock()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	// Передаем 0 как userID и b.config.MaxMessages как лимит
	messages, err := b.storage.GetMessagesSince(ctx, chatID, 0, sinceTime, b.config.MaxMessages)
	if err != nil {
		log.Printf("[ERROR][createAndSendSummary] Chat %d: Ошибка получения сообщений: %v", chatID, err)
		b.sendReply(chatID, "❌ Ошибка при получении сообщений для саммари.")
		editText = fmt.Sprintf("❌ Ошибка при получении сообщений для саммари: %v", err)
		sendText = editText
		b.updateOrSendMessage(chatID, lastInfoMsgID, editText, sendText, "") // Заменяем вызов
		return
	}

	if len(messages) == 0 {
		log.Printf("[INFO][createAndSendSummary] Чат %d: Нет сообщений за последние 24 часа.", chatID)
		editText = "Недостаточно сообщений за последние 24 часа для создания саммари."
		sendText = editText
		b.updateOrSendMessage(chatID, lastInfoMsgID, editText, sendText, "") // Заменяем вызов
		return
	}

	// --- Формируем контекст для саммари, используя историю и профили ---
	contextText := formatHistoryWithProfiles(
		chatID,
		messages,
		b.storage,
		b.config,
		b.config.TimeZone,
	)

	if contextText == "" {
		log.Printf("[WARN][createAndSendSummary] Чат %d: Контекст для саммари оказался пустым.", chatID)
		editText = "Не удалось подготовить данные для саммари (контекст пуст)."
		sendText = editText
		b.updateOrSendMessage(chatID, lastInfoMsgID, editText, sendText, "") // Заменяем вызов
		return
	}

	if b.config.Debug {
		log.Printf("[DEBUG][createAndSendSummary] Чат %d: Контекст для саммари (%d символов): %s...", chatID, len(contextText), truncateString(contextText, 200))
	}

	// --- Генерируем саммари ---
	log.Printf("[INFO][createAndSendSummary] Чат %d: Запрос генерации саммари к LLM (%s)...", chatID, b.config.LLMProvider)
	summaryStartTime := time.Now()

	// Выбираем правильный клиент LLM для генерации (b.llm - это интерфейс)
	generatedSummary, errLLM := b.llm.GenerateArbitraryResponse(b.config.SummaryPrompt, contextText)

	summaryDuration := time.Since(summaryStartTime)
	if errLLM != nil {
		log.Printf("[ERROR][createAndSendSummary] Чат %d: Ошибка генерации саммари: %v (за %v)", chatID, errLLM, summaryDuration)
		editText = fmt.Sprintf("❌ Ошибка генерации саммари (%v): %v", summaryDuration.Round(time.Second), errLLM)
		sendText = editText
		// Не обновляем LastAutoSummaryTime, чтобы попробовать позже
		b.updateOrSendMessage(chatID, lastInfoMsgID, editText, sendText, "") // Заменяем вызов
		return
	}

	log.Printf("[INFO][createAndSendSummary] Чат %d: Саммари успешно сгенерировано LLM (%s) за %v", chatID, b.config.LLMProvider, summaryDuration)
	if b.config.Debug {
		log.Printf("[DEBUG][Summary Raw] Chat %d: \n---START RAW---\n%s\n---END RAW---", chatID, generatedSummary)
	}

	// --- Финальная обработка и форматирование ---
	finalSummary := strings.TrimSpace(generatedSummary)

	// Определяем ParseMode на основе содержимого (простая эвристика)
	if strings.ContainsAny(finalSummary, "_*[]()~`>#+-=|{}.!") {
		// Если есть символы, требующие экранирования для MarkdownV2,
		// экранируем и используем MarkdownV2.
		escapedSummary := escapeMarkdownV2(finalSummary)
		if b.config.Debug {
			log.Printf("[DEBUG][Summary Escaped] Chat %d: \n---START ESCAPED---\n%s\n---END ESCAPED---", chatID, escapedSummary)
		}
		finalSummary = escapedSummary // Используем экранированный текст
		parseMode = "MarkdownV2"
		if b.config.Debug {
			log.Printf("[DEBUG][createAndSendSummary] Chat %d: Используем ParseMode=MarkdownV2 из-за найденных спецсимволов.", chatID)
		}
	} else {
		// Если спецсимволов нет, отправляем как обычный текст
		parseMode = ""
		if b.config.Debug {
			log.Printf("[DEBUG][createAndSendSummary] Chat %d: Используем ParseMode='' (обычный текст).", chatID)
		}
	}

	// Добавляем статусную строку в начало
	statusText := "📋 Саммари диалога за последние 24 часа:"
	// Используем переменную finalSummary, которая уже содержит либо оригинальный, либо экранированный текст
	fullSummaryText := statusText + "\n\n" + finalSummary

	// Ограничение длины сообщения Telegram
	const telegramMessageLimit = 4096
	if len(fullSummaryText) > telegramMessageLimit {
		log.Printf("[WARN][createAndSendSummary] Чат %d: Сгенерированное саммари (%d символов) превышает лимит Telegram (%d). Обрезаю.", chatID, len(fullSummaryText), telegramMessageLimit)
		fullSummaryText = truncateStringEnd(fullSummaryText, telegramMessageLimit)
		// Если обрезали, лучше убрать Markdown, чтобы не сломать разметку
		parseMode = ""
	}

	editText = fullSummaryText
	sendText = fullSummaryText

	// --- Обновляем время последнего авто-саммари (если это был авто-запуск) ---
	// Логика определения, был ли это авто-запуск, здесь неявная.
	// Будем считать, что если функция вызвана, то время нужно обновить.
	// Правильнее было бы передавать флаг isAuto.
	// Но пока обновим в любом случае при успешной генерации.
	b.settingsMutex.Lock()
	if settings, exists := b.chatSettings[chatID]; exists {
		settings.LastAutoSummaryTime = time.Now()
		log.Printf("[DEBUG][createAndSendSummary] Чат %d: Обновлено LastAutoSummaryTime на %v", chatID, settings.LastAutoSummaryTime.Format(time.Kitchen))
	} else {
		log.Printf("[WARN][createAndSendSummary] Чат %d: Настройки не найдены при попытке обновить LastAutoSummaryTime.", chatID)
	}
	b.settingsMutex.Unlock()

	// Обновляем или создаем сообщение
	b.updateOrSendMessage(chatID, lastInfoMsgID, editText, sendText, parseMode) // Заменяем вызов
}

// updateOrSendMessage пытается отредактировать сообщение, иначе отправляет новое.
// Сохраняет ID нового сообщения в chatSettings.LastInfoMessageID.
func (b *Bot) updateOrSendMessage(chatID int64, messageIDToEdit int, editText string, sendText string, parseMode string) {
	if messageIDToEdit != 0 {
		// Попытка редактирования
		editMsg := tgbotapi.NewEditMessageText(chatID, messageIDToEdit, editText)
		if parseMode != "" {
			editMsg.ParseMode = parseMode
		}
		_, err := b.api.Send(editMsg)
		if err == nil {
			if b.config.Debug {
				log.Printf("[DEBUG][updateOrSendMessage] Чат %d: Сообщение ID %d успешно отредактировано.", chatID, messageIDToEdit)
			}
			// ID сообщения остается прежним, не нужно обновлять chatSettings
			return // Успешно отредактировано, выходим
		}
		// Ошибка редактирования (например, сообщение слишком старое или удалено)
		log.Printf("[WARN][updateOrSendMessage] Чат %d: Не удалось отредактировать сообщение ID %d: %v. Отправляю новое.", chatID, messageIDToEdit, err)
		// Сбрасываем ID, чтобы отправить новое сообщение
		messageIDToEdit = 0
		// Удаляем старое ID из настроек, т.к. оно больше не актуально
		b.settingsMutex.Lock()
		if settings, exists := b.chatSettings[chatID]; exists && settings.LastInfoMessageID == messageIDToEdit {
			settings.LastInfoMessageID = 0
		}
		b.settingsMutex.Unlock()
	}

	// Отправка нового сообщения
	newMsg := tgbotapi.NewMessage(chatID, sendText)
	if parseMode != "" {
		newMsg.ParseMode = parseMode
	}
	sentMsg, err := b.api.Send(newMsg)
	if err != nil {
		log.Printf("[ERROR][updateOrSendMessage] Чат %d: Не удалось отправить новое сообщение: %v", chatID, err)
		return
	}

	// Сохраняем ID нового сообщения
	b.settingsMutex.Lock()
	if settings, exists := b.chatSettings[chatID]; exists {
		settings.LastInfoMessageID = sentMsg.MessageID
		if b.config.Debug {
			log.Printf("[DEBUG][updateOrSendMessage] Чат %d: Новое сообщение отправлено, ID сохранен: %d", chatID, sentMsg.MessageID)
		}
	} else {
		log.Printf("[WARN][updateOrSendMessage] Чат %d: Настройки не найдены при попытке сохранить ID нового сообщения %d.", chatID, sentMsg.MessageID)
	}
	b.settingsMutex.Unlock()
}

// --- Функции, перенесенные из helpers.go или специфичные для саммари ---

// truncateString обрезает строку до maxLen рун, добавляя "..."
// (Оставим здесь, т.к. используется в логах этого файла)
func truncateString(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen < 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}

// truncateStringEnd обрезает строку до maxLen рун без "..."
func truncateStringEnd(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen])
}
