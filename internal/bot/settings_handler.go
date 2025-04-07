package bot

import (
	"log"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// sendSettingsKeyboard отправляет клавиатуру настроек
func (b *Bot) sendSettingsKeyboard(chatID int64) {
	b.settingsMutex.RLock()
	settings, exists := b.chatSettings[chatID]
	if !exists {
		log.Printf("sendSettingsKeyboard: Настройки для чата %d не найдены!", chatID)
		b.settingsMutex.RUnlock()
		// Можно отправить сообщение об ошибке или создать настройки по умолчанию
		b.sendReply(chatID, "Ошибка: не удалось загрузить настройки чата.")
		return
	}

	b.settingsMutex.RUnlock()

	keyboard := getSettingsKeyboard(settings)
	b.sendReplyWithKeyboard(chatID, "⚙️ Настройки чата:", keyboard)
}

// updateSettingsKeyboard обновляет существующее сообщение с клавиатурой настроек
func (b *Bot) updateSettingsKeyboard(callback *tgbotapi.CallbackQuery) {
	if callback.Message == nil {
		return // Нечего обновлять
	}
	chatID := callback.Message.Chat.ID
	messageID := callback.Message.MessageID

	b.settingsMutex.RLock()
	settings, exists := b.chatSettings[chatID]
	if !exists {
		log.Printf("updateSettingsKeyboard: Настройки для чата %d не найдены!", chatID)
		b.settingsMutex.RUnlock()
		return
	}

	b.settingsMutex.RUnlock()

	newKeyboard := getSettingsKeyboard(settings)
	editMsg := tgbotapi.NewEditMessageReplyMarkup(chatID, messageID, newKeyboard)

	_, err := b.api.Request(editMsg)
	if err != nil {
		log.Printf("Ошибка обновления клавиатуры настроек: %v", err)
	}
}

// setSrachAnalysis включает или выключает анализ срачей для чата
func (b *Bot) setSrachAnalysis(chatID int64, enabled bool) {
	b.settingsMutex.Lock()
	defer b.settingsMutex.Unlock()

	if settings, exists := b.chatSettings[chatID]; exists {
		if settings.SrachAnalysisEnabled != enabled {
			settings.SrachAnalysisEnabled = enabled
			log.Printf("Чат %d: Анализ срачей %s.", chatID, getEnabledStatusText(settings.SrachAnalysisEnabled))
			// Сбрасываем состояние срача при изменении настройки
			settings.SrachState = "none"
			settings.SrachMessages = nil
		} else {
			log.Printf("Чат %d: Анализ срачей уже был %s.", chatID, getEnabledStatusText(settings.SrachAnalysisEnabled))
		}
	} else {
		log.Printf("setSrachAnalysis: Настройки для чата %d не найдены!", chatID)
	}
}
