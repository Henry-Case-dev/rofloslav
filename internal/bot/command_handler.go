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
	userID := message.From.ID
	username := message.From.UserName

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

	// Check if the user is an admin for admin-only commands
	isUserAdmin := b.isAdmin(message.From)

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
				// settings.LastInfoMessageID = sentMsg.MessageID // Обновляем settings через RLock/Lock
				// TODO: Проверить необходимость обновления LastInfoMessageID здесь, возможно не нужно
				if set, ok := b.chatSettings[chatID]; ok {
					set.LastInfoMessageID = sentMsg.MessageID
				}
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
			// settings.LastInfoMessageID = sentMsg.MessageID // Обновляем settings через RLock/Lock
			if set, ok := b.chatSettings[chatID]; ok {
				set.LastInfoMessageID = sentMsg.MessageID
			}
			b.settingsMutex.Unlock()
		} else {
			log.Printf("[ERROR] Ошибка отправки сообщения 'Генерирую саммари...' в чат %d: %v", chatID, err)
		}

		// Запускаем генерацию в горутине
		go b.createAndSendSummary(chatID)

	// --- Admin Command: /profile_set ---
	case "profile_set":
		if !isUserAdmin {
			b.sendReply(chatID, "🚫 У вас нет прав для выполнения этой команды.")
			return
		}

		// Инструкция по формату ввода
		instructionText := "📝 Введите данные профиля в следующем сообщении в формате:\\n`@никнейм - Короткое имя - Полное имя (если известно) - Био`\\n\\n_Это сообщение будет удалено через 15 секунд._"
		instructionMsg := tgbotapi.NewMessage(chatID, instructionText)
		instructionMsg.ParseMode = "Markdown"

		sentInstruction, err := b.api.Send(instructionMsg)
		if err != nil {
			log.Printf("[ERROR][CmdHandler /profile_set] Ошибка отправки инструкции в чат %d: %v", chatID, err)
			return
		}

		// Устанавливаем состояние ожидания ввода профиля
		b.settingsMutex.Lock()
		if settings, exists := b.chatSettings[chatID]; exists {
			settings.PendingSetting = "profile_data"               // Используем это поле для ожидания данных профиля
			settings.LastInfoMessageID = sentInstruction.MessageID // Сохраняем ID инструкции для последующего удаления
		}
		b.settingsMutex.Unlock()

		// Запускаем удаление инструкции через 15 секунд
		go func() {
			time.Sleep(15 * time.Second)
			b.deleteMessage(chatID, sentInstruction.MessageID)
			// Опционально: сбросить PendingSetting, если пользователь ничего не ввел за 15 сек?
			// Пока не будем, дадим время ввести.
		}()

		log.Printf("[ADMIN CMD] Пользователь %s (%d) инициировал команду /profile_set в чате %d. Ожидание ввода данных.", username, userID, chatID)
		// Выходим, основная логика будет в handleMessage при получении следующего сообщения

	default:
		// Проверяем, не админская ли это команда, чтобы не писать "Неизвестная команда" админам
		if !isUserAdmin {
			log.Printf("Неизвестная команда: %s от пользователя %s (%d) в чате %d", command, username, userID, chatID)
			// Можно отправить сообщение об ошибке или проигнорировать
			// b.sendReply(chatID, "Неизвестная команда.")
		} else {
			log.Printf("Неизвестная команда (от админа %s): %s", username, command)
			b.sendReply(chatID, "Неизвестная команда.")
		}
	}
}
