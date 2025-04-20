package bot

import (
	"log"
	"strings"

	"github.com/Henry-Case-dev/rofloslav/internal/utils" // Импортируем для TruncateString
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// sendReply отправляет текстовое сообщение в указанный чат.
// Логирует ошибки.
func (b *Bot) sendReply(chatID int64, text string) {
	msg := tgbotapi.NewMessage(chatID, text)
	_, err := b.api.Send(msg)
	if err != nil {
		// Улучшаем логирование ошибок API Telegram
		log.Printf("[ERROR] Ошибка отправки сообщения в чат %d: %v. Текст: %s...", chatID, err, utils.TruncateString(text, 50))
		// Дополнительная информация об ошибке, если доступна
		if tgErr, ok := err.(tgbotapi.Error); ok {
			log.Printf("[ERROR] Telegram API Error: Code %d, Description: %s", tgErr.Code, tgErr.Message)
		} else {
			log.Printf("[ERROR] Не удалось привести ошибку к типу tgbotapi.Error")
		}
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

// answerCallback отправляет ответ на CallbackQuery (например, уведомление при нажатии кнопки).
func (b *Bot) answerCallback(callbackID string, text string) {
	callbackConfig := tgbotapi.NewCallback(callbackID, text)
	_, err := b.api.Request(callbackConfig)
	if err != nil {
		log.Printf("Ошибка ответа на CallbackQuery (%s): %v", callbackID, err)
	}
}

// deleteMessage удаляет сообщение из чата.
func (b *Bot) deleteMessage(chatID int64, messageID int) {
	if messageID == 0 {
		log.Printf("[WARN] Попытка удаления сообщения с ID 0 в чате %d", chatID)
		return
	}
	deleteMsgConfig := tgbotapi.NewDeleteMessage(chatID, messageID)
	_, err := b.api.Request(deleteMsgConfig)
	if err != nil {
		// Часто бывает ошибка "message to delete not found", не будем ее сильно спамить
		if !strings.Contains(err.Error(), "message to delete not found") {
			log.Printf("[WARN] Ошибка удаления сообщения ID %d в чате %d: %v", messageID, chatID, err)
		} else {
			// Просто логируем в debug, если ошибка о не найденном сообщении
			if b.config.Debug {
				log.Printf("[DEBUG] Сообщение ID %d в чате %d не найдено для удаления (возможно, уже удалено).", messageID, chatID)
			}
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
