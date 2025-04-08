package bot

import (
	"fmt"
	"log"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// getMainKeyboard возвращает основную клавиатуру
func getMainKeyboard() tgbotapi.InlineKeyboardMarkup {
	return tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("📊 Саммари", "summary"),
			tgbotapi.NewInlineKeyboardButtonData("⚙️ Настройки", "settings"),
		),
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("⏸️ Пауза", "stop"),
		),
	)
}

// sendMainMenu удаляет старое меню (если есть) и отправляет новое
func (b *Bot) sendMainMenu(chatID int64, oldMenuID int) {
	// 1. Удаляем старое меню
	if oldMenuID != 0 {
		b.deleteMessage(chatID, oldMenuID)
	}

	// 2. Собираем текст сообщения
	modelInfo := fmt.Sprintf("Текущая модель: %s (%s)", b.config.LLMProvider, b.getCurrentModelName())
	text := "Главное меню:\n" + modelInfo

	// 3. Отправляем новое меню
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = getMainKeyboard()

	sentMsg, err := b.api.Send(msg)
	if err != nil {
		log.Printf("[ERROR][sendMainMenu] Ошибка отправки главного меню в чат %d: %v", chatID, err)
		return
	}

	// 4. Сохраняем ID нового меню
	b.settingsMutex.Lock()
	if settings, exists := b.chatSettings[chatID]; exists {
		settings.LastMenuMessageID = sentMsg.MessageID
	} else {
		log.Printf("[WARN][sendMainMenu] Настройки для чата %d не найдены при попытке сохранить LastMenuMessageID", chatID)
	}
	b.settingsMutex.Unlock()
}
