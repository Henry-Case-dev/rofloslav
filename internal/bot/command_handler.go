package bot

import (
	"log"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const summaryRequestInterval = 10 * time.Minute // Ограничение на вызов /summary

// handleCommand обрабатывает команды
func (b *Bot) handleCommand(message *tgbotapi.Message) {
	command := message.Command()
	chatID := message.Chat.ID

	// Get current settings for the chat
	b.settingsMutex.RLock()
	settings, exists := b.chatSettings[chatID]
	if !exists {
		// If settings don't exist, they should be created in handleUpdate before this
		log.Printf("[ERROR][CmdHandler] Chat %d: Settings not found for command /%s", chatID, command)
		b.settingsMutex.RUnlock()
		return
	}
	lastMenuMsgID := settings.LastMenuMessageID
	lastSettingsMsgID := settings.LastSettingsMessageID
	lastInfoMsgID := settings.LastInfoMessageID
	b.settingsMutex.RUnlock()

	// Delete the command message itself to keep the chat clean
	b.deleteMessage(chatID, message.MessageID)

	switch command {
	case "start":
		// Usually handled by ensureChatInitializedAndWelcome
		// Send main menu anyway
		b.sendMainMenu(chatID, lastMenuMsgID)
	case "menu":
		b.sendMainMenu(chatID, lastMenuMsgID)
	case "settings":
		b.sendSettingsKeyboard(chatID, lastSettingsMsgID)
	case "summary":
		// Check rate limit
		now := time.Now()
		b.summaryMutex.Lock() // Используем мьютекс для lastSummaryRequest
		lastReq, ok := b.lastSummaryRequest[chatID]
		if ok && now.Sub(lastReq) < summaryRequestInterval {
			log.Printf("[DEBUG] Чат %d: /summary отклонен из-за rate limit. Прошло: %v < %v", chatID, now.Sub(lastReq), summaryRequestInterval)
			b.summaryMutex.Unlock()
			// --- Удаляем предыдущее инфо-сообщение перед отправкой нового ---
			if lastInfoMsgID != 0 {
				b.deleteMessage(chatID, lastInfoMsgID)
			}
			// --- Отправляем сообщение об ошибке и сохраняем его ID ---
			msg := tgbotapi.NewMessage(chatID, b.config.RateLimitErrorMessage)
			sentMsg, err := b.api.Send(msg)
			if err == nil {
				b.settingsMutex.Lock()
				settings.LastInfoMessageID = sentMsg.MessageID // Обновляем settings через RLock/Lock
				b.settingsMutex.Unlock()
			}
			return
		}
		// Update last request time
		b.lastSummaryRequest[chatID] = now
		b.summaryMutex.Unlock()

		log.Printf("[DEBUG] Чат %d: /summary вызван. Последний запрос был: %v (ok=%t). Прошло: %v. Лимит: %v.",
			chatID, lastReq, ok, now.Sub(lastReq), summaryRequestInterval)

		// --- Удаляем предыдущее инфо-сообщение перед отправкой нового ---
		if lastInfoMsgID != 0 {
			b.deleteMessage(chatID, lastInfoMsgID)
		}
		// --- Отправляем сообщение о начале генерации и сохраняем его ID ---
		msg := tgbotapi.NewMessage(chatID, "Генерирую саммари, подождите...")
		sentMsg, err := b.api.Send(msg)
		if err == nil {
			b.settingsMutex.Lock()
			settings.LastInfoMessageID = sentMsg.MessageID // Обновляем settings через RLock/Lock
			b.settingsMutex.Unlock()
		} else {
			log.Printf("[ERROR] Ошибка отправки сообщения 'Генерирую саммари...' в чат %d: %v", chatID, err)
		}

		// Запускаем генерацию в горутине
		go b.createAndSendSummary(chatID)
	default:
		log.Printf("Неизвестная команда: %s от пользователя %d в чате %d", command, message.From.ID, chatID)
		// Можно отправить сообщение об ошибке или проигнорировать
	}
}
