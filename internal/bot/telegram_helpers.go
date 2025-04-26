package bot

import (
	"log"
	"strings"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/utils" // Импортируем для TruncateString
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// sendReply отправляет простое текстовое сообщение в ответ.
func (b *Bot) sendReply(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	_, err := b.api.Send(msg)
	if err != nil {
		log.Printf("Ошибка отправки ответа в чат %d: %v", chatID, err)
	}
}

// sendReplyTo отправляет текстовое сообщение в ответ на конкретное сообщение.
func (b *Bot) sendReplyTo(chatID int64, messageID int, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyToMessageID = messageID
	_, err := b.api.Send(msg)
	if err != nil {
		log.Printf("Ошибка отправки ответа (ReplyTo %d) в чат %d: %v", messageID, chatID, err)
	}
}

// deleteMessage удаляет сообщение.
func (b *Bot) deleteMessage(chatID int64, messageID int) {
	deleteReq := tgbotapi.NewDeleteMessage(chatID, messageID)
	_, err := b.api.Request(deleteReq) // Используем Request для DeleteMessage
	if err != nil {
		// Не логируем как ошибку, если сообщение уже удалено или слишком старое
		if !strings.Contains(err.Error(), "message to delete not found") && !strings.Contains(err.Error(), "message can't be deleted") {
			log.Printf("[WARN] Ошибка удаления сообщения ID %d в чате %d: %v", messageID, chatID, err)
		}
	}
}

// sendAndDeleteAfter отправляет сообщение и удаляет его через указанную задержку.
func (b *Bot) sendAndDeleteAfter(chatID int64, text string, delay time.Duration) {
	msg := tgbotapi.NewMessage(chatID, text)
	sentMsg, err := b.api.Send(msg)
	if err != nil {
		log.Printf("[ERROR][sendAndDeleteAfter] Чат %d: Не удалось отправить сообщение '%s': %v", chatID, text, err)
		return
	}

	// Запускаем таймер на удаление
	time.AfterFunc(delay, func() {
		b.deleteMessage(chatID, sentMsg.MessageID)
		if b.config.Debug {
			log.Printf("[DEBUG][sendAndDeleteAfter] Чат %d: Автоматически удалено сообщение ID %d ('%s...')", chatID, sentMsg.MessageID, utils.TruncateString(text, 30))
		}
	})
}

// answerCallback отвечает на CallbackQuery (например, подтверждает нажатие кнопки).
func (b *Bot) answerCallback(callbackID string, text string) {
	callbackConfig := tgbotapi.NewCallback(callbackID, text)
	_, err := b.api.Request(callbackConfig)
	if err != nil {
		log.Printf("Ошибка ответа на CallbackQuery (%s): %v", callbackID, err)
	}
}

// sendReplyMarkdown отправляет текстовое сообщение в указанный чат с поддержкой Markdown.
// Логирует ошибки.
func (b *Bot) sendReplyMarkdown(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = "Markdown" // Устанавливаем ParseMode
	_, err := b.api.Send(msg)
	if err != nil {
		// Улучшаем логирование ошибок API Telegram
		log.Printf("[ERROR] Ошибка отправки Markdown сообщения в чат %d: %v. Текст: %s...", chatID, err, utils.TruncateString(text, 50))
		// Дополнительная информация об ошибке, если доступна
		if tgErr, ok := err.(tgbotapi.Error); ok {
			log.Printf("[ERROR] Telegram API Error: Code %d, Description: %s", tgErr.Code, tgErr.Message)
		} else {
			log.Printf("[ERROR] Не удалось привести ошибку к типу tgbotapi.Error")
		}
	}
}

// sendReplyReturnMsg - вспомогательная функция для отправки сообщения и возврата его объекта
func (b *Bot) sendReplyReturnMsg(chatID int64, text string) (*tgbotapi.Message, error) {
	msg := tgbotapi.NewMessage(chatID, text)
	// msg.ParseMode = "Markdown" // Убрано, т.к. не всегда нужен Markdown
	sentMsg, err := b.api.Send(msg)
	if err != nil {
		log.Printf("Ошибка отправки сообщения (возврат объекта) в чат %d: %v", chatID, err)
		return nil, err
	}
	return &sentMsg, nil
}

// sendReplyAndDeleteInitial - отправляет итоговое сообщение и удаляет исходное
func (b *Bot) sendReplyAndDeleteInitial(chatID int64, finalMsgText string, initialMsgID int) {
	// Отправляем итоговое сообщение
	b.sendReply(chatID, finalMsgText) // Вызов перенесенного sendReply

	// Удаляем начальное сообщение, если его ID был сохранен
	if initialMsgID != 0 {
		b.deleteMessage(chatID, initialMsgID) // Вызов перенесенного deleteMessage
	}
}
