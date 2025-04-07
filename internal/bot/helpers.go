package bot

import (
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// sendReply отправляет текстовое сообщение в указанный чат.
// Использует Markdown для форматирования.
func (b *Bot) sendReply(chatID int64, text string) {
	if text == "" {
		log.Printf("[WARN] Попытка отправить пустое сообщение в чат %d", chatID)
		return
	}
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown" // Используем Markdown

	_, err := b.api.Send(msg)
	if err != nil {
		// Улучшаем логирование ошибок API Telegram
		log.Printf("[ERROR] Ошибка отправки сообщения в чат %d: %v. Текст: %s...", chatID, err, truncateString(text, 50))
		// Дополнительная информация об ошибке, если доступна
		if tgErr, ok := err.(tgbotapi.Error); ok {
			log.Printf("[ERROR] Telegram API Error: Code %d, Description: %s", tgErr.Code, tgErr.Message)
		}
	}
}

// sendReplyWithKeyboard отправляет текстовое сообщение с inline-клавиатурой.
// Использует Markdown для форматирования.
func (b *Bot) sendReplyWithKeyboard(chatID int64, text string, keyboard tgbotapi.InlineKeyboardMarkup) {
	if text == "" {
		log.Printf("[WARN] Попытка отправить пустое сообщение с клавиатурой в чат %d", chatID)
		return
	}
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown" // Используем Markdown
	msg.ReplyMarkup = keyboard

	_, err := b.api.Send(msg)
	if err != nil {
		log.Printf("[ERROR] Ошибка отправки сообщения с клавиатурой в чат %d: %v. Текст: %s...", chatID, err, truncateString(text, 50))
		if tgErr, ok := err.(tgbotapi.Error); ok {
			log.Printf("[ERROR] Telegram API Error: Code %d, Description: %s", tgErr.Code, tgErr.Message)
		}
	}
}

// answerCallback отправляет ответ на CallbackQuery (например, уведомление при нажатии кнопки).
func (b *Bot) answerCallback(callbackID string, text string) {
	callback := tgbotapi.NewCallback(callbackID, text)
	// Не показываем alert по умолчанию (ShowAlert: false)
	_, err := b.api.Request(callback)
	if err != nil {
		// Эта ошибка менее критична, чем отправка сообщения, можно логировать с меньшим уровнем.
		log.Printf("[WARN] Ошибка ответа на callback %s: %v", callbackID, err)
	}
}

// truncateString обрезает строку до указанной максимальной длины (в рунах),
// добавляя "..." в конце, если строка была обрезана.
// Безопасно для Unicode.
func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	// Обеспечиваем минимальную длину для добавления "..."
	if maxLen < 3 {
		if maxLen <= 0 {
			return ""
		}
		return string(runes[:maxLen])
	}
	// Обрезаем и добавляем троеточие
	return string(runes[:maxLen-3]) + "..."
}

// formatDuration форматирует time.Duration в более читаемый вид (например, "5m10s").
func formatDuration(d time.Duration) string {
	return d.Round(time.Second).String()
}

// min возвращает меньшее из двух целых чисел.
func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// max возвращает большее из двух целых чисел.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// formatMessageForAnalysis форматирует сообщение для передачи в LLM при анализе срача
// или для логов. Включает имя пользователя и информацию об ответе.
func formatMessageForAnalysis(msg *tgbotapi.Message) string {
	if msg == nil {
		return "[пустое сообщение]"
	}
	userName := "UnknownUser"
	if msg.From != nil {
		userName = msg.From.UserName
		if userName == "" {
			userName = strings.TrimSpace(msg.From.FirstName + " " + msg.From.LastName)
		}
	}
	// Добавляем информацию об ответе, если есть
	replyInfo := ""
	if msg.ReplyToMessage != nil && msg.ReplyToMessage.From != nil {
		replyUser := msg.ReplyToMessage.From.UserName
		if replyUser == "" {
			replyUser = strings.TrimSpace(msg.ReplyToMessage.From.FirstName + " " + msg.ReplyToMessage.From.LastName)
		}
		replyInfo = fmt.Sprintf(" (ответ %s)", replyUser)
	}

	// Обрабатываем текст
	text := msg.Text
	if text == "" {
		if msg.Caption != "" {
			text = fmt.Sprintf("[Подпись к медиа: %s]", truncateString(msg.Caption, 30))
		} else if msg.Sticker != nil {
			text = fmt.Sprintf("[Стикер: %s]", msg.Sticker.Emoji)
		} else if len(msg.Photo) > 0 {
			text = "[Фото]"
		} else if msg.Video != nil {
			text = "[Видео]"
		} else if msg.Voice != nil {
			text = "[Голосовое сообщение]"
		} else if msg.Document != nil {
			text = fmt.Sprintf("[Документ: %s]", msg.Document.FileName)
		} else {
			text = "[Нетекстовое сообщение]"
		}
	}

	return fmt.Sprintf("[%s]%s: %s", userName, replyInfo, text)
}
