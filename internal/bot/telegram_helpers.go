package bot

import (
	"fmt"
	"log"
	"strings"
	"time"
	"unicode/utf8"

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
	if messageID == 0 {
		return // Игнорируем удаление с ID = 0
	}

	deleteReq := tgbotapi.NewDeleteMessage(chatID, messageID)
	_, err := b.api.Request(deleteReq) // Используем Request для DeleteMessage
	if err != nil {
		// Только логируем ошибку, но не выбрасываем её дальше
		log.Printf("[WARN] Ошибка удаления сообщения ID %d в чате %d: %v", messageID, chatID, err)
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
	callback := tgbotapi.NewCallback(callbackID, text)
	// Если текст слишком длинный, обрезаем его до 200 символов
	if utf8.RuneCountInString(text) > 200 {
		callback.Text = string([]rune(text)[:197]) + "..."
	}
	_, err := b.api.Request(callback)
	if err != nil {
		log.Printf("Ошибка ответа на CallbackQuery (%s): %v", callbackID, err)
	}
}

// sendReplyMarkdown отправляет текстовое сообщение в указанный чат с поддержкой Markdown.
// Логирует ошибки.
func (b *Bot) sendReplyMarkdown(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ParseMode = tgbotapi.ModeMarkdown
	_, err := b.api.Send(msg)
	if err != nil {
		// Если ошибка связана с Markdown, отправляем обычный текст
		if strings.Contains(err.Error(), "markdown") || strings.Contains(err.Error(), "parse") {
			log.Printf("[ERROR] Ошибка отправки Markdown сообщения в чат %d: %v. Текст: %s...", chatID, err, utils.TruncateString(text, 50))
			plainText := tgbotapi.NewMessage(chatID, text)
			b.api.Send(plainText)
		}
	}
}

// sendReplyReturnMsg - вспомогательная функция для отправки сообщения и возврата его объекта
func (b *Bot) sendReplyReturnMsg(chatID int64, text string) (*tgbotapi.Message, error) {
	msg := tgbotapi.NewMessage(chatID, text)
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

// sendAutoDeleteErrorReply отправляет сообщение об ошибке, которое автоматически удаляется через указанное время
func (b *Bot) sendAutoDeleteErrorReply(chatID int64, replyToMessageID int, errorText string) {
	msg := tgbotapi.NewMessage(chatID, errorText)
	if replyToMessageID != 0 {
		msg.ReplyToMessageID = replyToMessageID
	}

	sentMsg, err := b.api.Send(msg)
	if err != nil {
		log.Printf("[ERROR] Не удалось отправить сообщение об ошибке в чат %d: %v", chatID, err)
		return
	}

	// Запускаем горутину для автоудаления сообщения
	go func(cID int64, mID int, delaySeconds int) {
		if delaySeconds <= 0 {
			return // Не удаляем, если задержка <= 0
		}

		time.Sleep(time.Duration(delaySeconds) * time.Second)
		b.deleteMessage(cID, mID)
		if b.config.Debug {
			log.Printf("[DEBUG] Автоматически удалено сообщение об ошибке (ID: %d) в чате %d", mID, cID)
		}
	}(chatID, sentMsg.MessageID, b.config.ErrorMessageAutoDeleteSeconds)
}

// getBotMember получает информацию о боте как участнике чата
func (b *Bot) getBotMember(chatID int64) (*tgbotapi.ChatMember, error) {
	memberConfig := tgbotapi.GetChatMemberConfig{
		ChatConfigWithUser: tgbotapi.ChatConfigWithUser{
			ChatID: chatID,
			UserID: b.api.Self.ID,
		},
	}
	member, err := b.api.GetChatMember(memberConfig)
	if err != nil {
		return nil, fmt.Errorf("ошибка получения информации о боте в чате %d: %w", chatID, err)
	}
	return &member, nil
}

// sendAutoDeleteMessage отправляет сообщение и удаляет его через указанную задержку
func (b *Bot) sendAutoDeleteMessage(chatID int64, text string, delay time.Duration) {
	msg := tgbotapi.NewMessage(chatID, text)
	sentMsg, err := b.api.Send(msg)
	if err != nil {
		log.Printf("[sendAutoDeleteMessage ERROR] Чат %d: Ошибка отправки сообщения для автоудаления: %v", chatID, err)
		return
	}

	// Запускаем таймер для удаления сообщения
	time.AfterFunc(delay, func() {
		b.deleteMessage(chatID, sentMsg.MessageID)
	})
}
