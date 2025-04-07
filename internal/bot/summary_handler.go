package bot

import (
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// generateSummary создает и отправляет саммари диалога
func (b *Bot) generateSummary(chatID int64) {
	// Получаем сообщения за последние 24 часа
	messages := b.storage.GetMessagesSince(chatID, time.Now().Add(-24*time.Hour))
	if len(messages) == 0 {
		b.sendReply(chatID, "Недостаточно сообщений для создания саммари.")
		return
	}

	if b.config.Debug {
		log.Printf("[DEBUG] Создаю саммари для чата %d. Найдено сообщений: %d", chatID, len(messages))
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

		// Отправляем запрос к Gemini с промптом для саммари
		summary, err := b.llm.GenerateResponse(summaryPrompt, messages)
		if err != nil {
			lastErr = err // Сохраняем последнюю ошибку
			if b.config.Debug {
				log.Printf("[DEBUG] Чат %d: Ошибка при генерации саммари (попытка %d): %v", chatID, attempt, err)
			}
			// При ошибке API нет смысла повторять сразу без паузы
			if attempt < maxAttempts {
				time.Sleep(1 * time.Second)
			}
			continue // Переходим к следующей попытке
		}

		// Проверяем количество слов
		wordCount := len(strings.Fields(summary))
		if b.config.Debug {
			log.Printf("[DEBUG] Чат %d: Сгенерировано саммари (попытка %d), слов: %d. Текст: %s...", chatID, attempt, wordCount, summary)
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

	// Проверяем результат после всех попыток
	if finalSummary == "" {
		if b.config.Debug {
			log.Printf("[DEBUG] Чат %d: Не удалось сгенерировать достаточно длинное саммари после %d попыток.", chatID, maxAttempts)
		}
		errMsg := "Не удалось создать достаточно информативное саммари после нескольких попыток."
		if lastErr != nil { // Если последняя попытка завершилась ошибкой API или предыдущие были неудачными
			errMsg += fmt.Sprintf(" Последняя ошибка: %v", lastErr)
		}
		b.sendReply(chatID, errMsg)
		return
	}

	if b.config.Debug {
		log.Printf("[DEBUG] Саммари успешно создано для чата %d после %d попыток", chatID, attempt)
	}

	// Отправляем финальное саммари
	finalMessageText := fmt.Sprintf("📋 *Саммари диалога за последние 24 часа:*\n\n%s", finalSummary)
	msg := tgbotapi.NewMessage(chatID, finalMessageText)
	msg.ParseMode = "Markdown"
	_, sendErr := b.api.Send(msg)
	if sendErr != nil {
		log.Printf("Ошибка отправки финального саммари в чат %d: %v", chatID, sendErr)
	}
}
