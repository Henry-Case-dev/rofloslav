package bot

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// === Вспомогательные функции для саммари ===

// createAndSendSummary генерирует и отправляет саммари чата
func (b *Bot) createAndSendSummary(chatID int64) {
	log.Printf("[Summary START] Chat %d: Начало генерации саммари...", chatID)
	startTime := time.Now()

	// 1. Получаем ID последнего информационного сообщения (чтобы потом его обновить)
	b.settingsMutex.RLock()
	settings, exists := b.chatSettings[chatID]
	if !exists {
		b.settingsMutex.RUnlock()
		log.Printf("[Summary ERROR] Chat %d: Настройки не найдены в памяти!", chatID)
		// Попытаться отправить сообщение об ошибке?
		return
	}
	lastInfoMsgID := settings.LastInfoMessageID
	b.settingsMutex.RUnlock()

	// 2. Получаем историю сообщений за последние 24 часа
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute) // Таймаут на получение истории
	defer cancel()

	history, err := b.storage.GetMessagesSince(ctx, chatID, 0, time.Now().Add(-24*time.Hour), 5000) // Лимит 5000 для саммари
	if err != nil {
		log.Printf("[Summary ERROR] Chat %d: Ошибка получения истории: %v", chatID, err)
		b.updateOrSendMessage(chatID, lastInfoMsgID, "❌ Ошибка получения истории чата для саммари.", "❌ Ошибка получения истории чата для саммари.", "")
		return
	}

	if len(history) == 0 {
		log.Printf("[Summary INFO] Chat %d: Нет сообщений за последние 24 часа для саммари.", chatID)
		b.updateOrSendMessage(chatID, lastInfoMsgID, "🤷 За последние 24 часа нет сообщений для саммари.", "🤷 За последние 24 часа нет сообщений для саммари.", "")
		return
	}

	// 3. Форматируем историю с профилями
	formattedHistory := formatHistoryWithProfiles(chatID, history, b.storage, b.config, b.config.TimeZone)

	// 4. Генерируем саммари с помощью LLM
	var summary string
	llmStartTime := time.Now()
	maxRetries := 3
	retryDelay := 5 * time.Second

	for i := 0; i < maxRetries; i++ {
		summary, err = b.llm.GenerateArbitraryResponse(b.config.SummaryPrompt, formattedHistory)
		if err == nil {
			break // Успех
		}

		// Проверяем на ошибку rate limit (429)
		if strings.Contains(err.Error(), "429") || strings.Contains(strings.ToLower(err.Error()), "rate limit") {
			log.Printf("[Summary WARN] Chat %d: Ошибка Rate Limit (429) при генерации саммари (Попытка %d/%d). Ожидание %v...", chatID, i+1, maxRetries, retryDelay)
			time.Sleep(retryDelay)
			retryDelay *= 2 // Экспоненциальная задержка
		} else {
			// Другая ошибка, прекращаем попытки
			log.Printf("[Summary ERROR] Chat %d: Неисправимая ошибка LLM при генерации саммари: %v", chatID, err)
			break
		}
	}

	// Если после всех попыток ошибка все еще есть
	if err != nil {
		errMsg := fmt.Sprintf("❌ Ошибка генерации саммари: %v", err)
		if strings.Contains(err.Error(), "429") {
			errMsg = fmt.Sprintf("❌ Ошибка генерации саммари: Превышен лимит запросов к LLM (%d попыток).", maxRetries)
		}
		b.updateOrSendMessage(chatID, lastInfoMsgID, errMsg, errMsg, "")
		return
	}

	llmDuration := time.Since(llmStartTime)

	if summary == "" {
		log.Printf("[Summary WARN] Chat %d: LLM вернул пустое саммари.", chatID)
		b.updateOrSendMessage(chatID, lastInfoMsgID, "🤔 LLM не смог сгенерировать саммари (вернул пустой ответ).", "🤔 LLM не смог сгенерировать саммари (вернул пустой ответ).", "")
		return
	}

	// 5. Подготовка к отправке
	finalSummary := strings.TrimSpace(summary)

	// --- Логика выбора ParseMode и экранирования УДАЛЕНА ---
	// Всегда используем стандартный Markdown
	parseMode := tgbotapi.ModeMarkdown

	// 6. Отправка или обновление сообщения
	// Обрезаем, если слишком длинное (Telegram лимит 4096)
	const telegramMaxMsgLen = 4096
	truncatedText := finalSummary
	wasTruncated := false
	if utf8.RuneCountInString(finalSummary) > telegramMaxMsgLen {
		truncatedText = truncateStringEnd(finalSummary, telegramMaxMsgLen)
		wasTruncated = true
		// Если обрезали, лучше убрать Markdown, чтобы не сломать разметку
		// Но мы перешли на стандартный Markdown, который менее чувствителен.
		// Оставим Markdown, но предупредим в логе.
		log.Printf("[Summary WARN] Chat %d: Саммари было обрезано до %d символов (было %d). Отправляется с ParseMode=Markdown.", chatID, telegramMaxMsgLen, utf8.RuneCountInString(finalSummary))
	}

	// Собираем текст сообщения для обновления или отправки
	// (Предполагаем, что updateOrSendMessage обработает обновление существующего сообщения)
	messageTextToSend := truncatedText // Текст для нового сообщения (если lastInfoMsgID=0 или обновление не удалось)
	editMessageText := truncatedText   // Текст для редактирования

	b.updateOrSendMessage(chatID, lastInfoMsgID, editMessageText, messageTextToSend, parseMode)

	// 7. Очищаем LastInfoMessageID после успешной отправки/обновления
	b.settingsMutex.Lock()
	if settings, exists := b.chatSettings[chatID]; exists {
		settings.LastInfoMessageID = 0
	}
	b.settingsMutex.Unlock()

	// Логирование завершения
	totalDuration := time.Since(startTime)
	log.Printf("[Summary COMPLETE] Chat %d: Саммари сгенерировано и отправлено. LLM: %v, Total: %v. Truncated: %t",
		chatID, llmDuration, totalDuration, wasTruncated)
}

// updateOrSendMessage пытается обновить сообщение messageIDToEdit текстом editText.
// Если messageIDToEdit = 0 или обновление не удается, отправляет новое сообщение с текстом sendText.
func (b *Bot) updateOrSendMessage(chatID int64, messageIDToEdit int, editText string, sendText string, parseMode string) {
	updated := false
	if messageIDToEdit != 0 {
		// --- Проверяем, существует ли сообщение перед редактированием ---
		// Это не идеальный способ, но может помочь избежать некоторых ошибок
		// Лучше обрабатывать конкретную ошибку "message to edit not found", но API может ее не возвращать явно.
		// Попытка редактирования:
		msg := tgbotapi.NewEditMessageText(chatID, messageIDToEdit, editText)
		msg.ParseMode = parseMode
		_, err := b.api.Send(msg)
		if err == nil {
			updated = true
			log.Printf("[DEBUG][updateOrSendMessage] Chat %d: Сообщение %d успешно обновлено.", chatID, messageIDToEdit)
		} else {
			// Проверяем на распространенные ошибки, которые означают, что нужно отправить новое сообщение
			errMsg := err.Error()
			if strings.Contains(errMsg, "message to edit not found") ||
				strings.Contains(errMsg, "message can't be edited") ||
				strings.Contains(errMsg, "message identifier is not specified") ||
				strings.Contains(errMsg, "message is not modified") { // Добавлено: если сообщение не изменилось
				log.Printf("[INFO][updateOrSendMessage] Chat %d: Не удалось обновить сообщение %d (%v), будет отправлено новое.", chatID, messageIDToEdit, err)
			} else {
				// Другая, возможно, более серьезная ошибка
				log.Printf("[ERROR][updateOrSendMessage] Chat %d: Неожиданная ошибка при обновлении сообщения %d: %v", chatID, messageIDToEdit, err)
			}
		}
	}

	if !updated {
		// Отправляем новое сообщение
		msg := tgbotapi.NewMessage(chatID, sendText)
		msg.ParseMode = parseMode
		_, err := b.api.Send(msg)
		if err != nil {
			log.Printf("[ERROR][updateOrSendMessage] Chat %d: Ошибка отправки нового сообщения: %v", chatID, err)
		}
	}
}

// --- Функции для обрезки строк (дублируются?) ---
// TODO: Вынести в utils или использовать существующие из utils?

// truncateString обрезает строку до максимальной длины, добавляя "..."
// func truncateString(s string, maxLen int) string {
// 	if utf8.RuneCountInString(s) <= maxLen {
// 		return s
// 	}
// 	if maxLen < 3 {
// 		return "..."[:maxLen] // Возвращаем часть "..."
// 	}
// 	runes := []rune(s)
// 	return string(runes[:maxLen-3]) + "..."
// }

// truncateStringEnd обрезает строку до максимальной длины без добавления "..."
func truncateStringEnd(s string, maxLen int) string {
	if maxLen <= 0 {
		return ""
	}
	if utf8.RuneCountInString(s) <= maxLen {
		return s
	}
	runes := []rune(s)
	return string(runes[:maxLen])
}
