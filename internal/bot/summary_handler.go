package bot

import (
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// createAndSendSummary создает и отправляет саммари диалога,
// редактируя существующее сообщение или отправляя новое.
func (b *Bot) createAndSendSummary(chatID int64) {
	// Получаем ID сообщения "Генерирую..."
	b.settingsMutex.RLock()
	settings, exists := b.chatSettings[chatID]
	var infoMessageID int
	if exists {
		infoMessageID = settings.LastInfoMessageID
	} else {
		log.Printf("[WARN][createAndSendSummary] Чат %d: Настройки не найдены, не могу получить LastInfoMessageID.", chatID)
	}
	b.settingsMutex.RUnlock()

	// --- Сообщение для редактирования/отправки ---
	var editText, sendText string // Текст для редактирования или нового сообщения
	parseMode := ""               // Режим парсинга (Markdown)

	// Получаем сообщения за последние 24 часа
	messages := b.storage.GetMessagesSince(chatID, time.Now().Add(-24*time.Hour))
	if len(messages) == 0 {
		editText = "Недостаточно сообщений за последние 24 часа для создания саммари."
		sendText = editText
		b.updateOrCreateMessage(chatID, infoMessageID, editText, sendText, parseMode)
		return
	}

	if b.config.Debug {
		log.Printf("[DEBUG] Создаю саммари для чата %d. Найдено сообщений: %d. Инфо-сообщение ID: %d", chatID, len(messages), infoMessageID)
	}

	// Используем только промпт для саммари без комбинирования
	summaryPrompt := b.config.SummaryPrompt

	const maxAttempts = 3 // Максимальное количество попыток генерации
	const minWords = 10   // Минимальное количество слов в саммари

	var finalSummary string
	var lastErr error // Сохраняем последнюю ошибку API
	var attempt int

	for attempt = 1; attempt <= maxAttempts; attempt++ {
		if b.config.Debug {
			log.Printf("[DEBUG] Чат %d: Попытка генерации саммари №%d", chatID, attempt)
		}

		// Отправляем запрос к LLM с промптом для саммари
		summary, err := b.llm.GenerateResponse(summaryPrompt, messages)
		if err != nil {
			lastErr = err // Сохраняем последнюю ошибку
			if b.config.Debug {
				log.Printf("[DEBUG] Чат %d: Ошибка при генерации саммари (попытка %d): %v", chatID, attempt, err)
			}
			if attempt < maxAttempts {
				time.Sleep(1 * time.Second)
			}
			continue // Переходим к следующей попытке
		}

		// Проверяем количество слов
		wordCount := len(strings.Fields(summary))
		if b.config.Debug {
			log.Printf("[DEBUG] Чат %d: Сгенерировано саммари (попытка %d), слов: %d.", chatID, attempt, wordCount)
		}

		if wordCount >= minWords {
			finalSummary = summary
			lastErr = nil // Сбрасываем ошибку при успехе
			break         // Успешная генерация, выходим из цикла
		}

		// Если слов мало, добавляем небольшую задержку перед следующей попыткой
		if attempt < maxAttempts {
			time.Sleep(1 * time.Second)
		}
	}

	// Формируем текст ответа в зависимости от результата
	if finalSummary != "" {
		if b.config.Debug {
			log.Printf("[DEBUG] Саммари успешно создано для чата %d после %d попыток", chatID, attempt)
		}
		editText = fmt.Sprintf("📋 *Саммари диалога за последние 24 часа:*\n\n%s", finalSummary)
		sendText = editText
		parseMode = "Markdown"
	} else {
		if b.config.Debug {
			log.Printf("[DEBUG] Чат %d: Не удалось сгенерировать достаточно длинное саммари после %d попыток.", chatID, maxAttempts)
		}
		errMsg := "Не удалось создать достаточно информативное саммари после нескольких попыток."
		if lastErr != nil {
			errMsg += fmt.Sprintf(" Последняя ошибка: %v", lastErr)
		}
		editText = errMsg
		sendText = editText
		parseMode = "" // Ошибки без форматирования
	}

	// Обновляем или создаем сообщение
	b.updateOrCreateMessage(chatID, infoMessageID, editText, sendText, parseMode)
}

// updateOrCreateMessage редактирует существующее сообщение или отправляет новое.
// Обновляет LastInfoMessageID при отправке нового сообщения.
func (b *Bot) updateOrCreateMessage(chatID int64, messageID int, editText, sendText, parseMode string) {
	if messageID != 0 {
		// Пытаемся отредактировать существующее сообщение
		editMsg := tgbotapi.NewEditMessageText(chatID, messageID, editText)
		if parseMode != "" {
			editMsg.ParseMode = parseMode
		}
		_, err := b.api.Send(editMsg)
		if err == nil {
			if b.config.Debug {
				log.Printf("[DEBUG][UpdateMsg] Сообщение %d в чате %d успешно отредактировано.", messageID, chatID)
			}
			return // Успешно отредактировано
		}
		// Если редактирование не удалось (например, сообщение удалено), логгируем и отправляем новое
		log.Printf("[WARN][UpdateMsg] Не удалось отредактировать сообщение %d в чате %d: %v. Отправляю новое.", messageID, chatID, err)
	} else {
		log.Printf("[DEBUG][UpdateMsg] MessageID == 0 для чата %d. Отправляю новое сообщение.", chatID)
	}

	// Отправляем новое сообщение
	msg := tgbotapi.NewMessage(chatID, sendText)
	if parseMode != "" {
		msg.ParseMode = parseMode
	}
	sentMsg, err := b.api.Send(msg)
	if err != nil {
		log.Printf("[ERROR][UpdateMsg] Ошибка отправки нового сообщения в чат %d: %v", chatID, err)
		return
	}

	// Сохраняем ID нового сообщения
	b.settingsMutex.Lock()
	if settings, exists := b.chatSettings[chatID]; exists {
		settings.LastInfoMessageID = sentMsg.MessageID
		log.Printf("[DEBUG][UpdateMsg] Сохранен новый LastInfoMessageID: %d для чата %d", sentMsg.MessageID, chatID)
	} else {
		log.Printf("[WARN][UpdateMsg] Настройки для чата %d не найдены при попытке сохранить новый LastInfoMessageID.", chatID)
	}
	b.settingsMutex.Unlock()
}
