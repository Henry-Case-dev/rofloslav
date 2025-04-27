package bot

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/config"
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
		durationSinceLast := now.Sub(lastReq)
		if ok && durationSinceLast < summaryRequestInterval {
			remainingTime := summaryRequestInterval - durationSinceLast
			log.Printf("[DEBUG] Чат %d: /summary отклонен из-за rate limit. Прошло: %v < %v. Осталось: %v", chatID, durationSinceLast, summaryRequestInterval, remainingTime)
			b.summaryMutex.Unlock()

			// --- Генерация динамической части сообщения ---
			dynamicPart := ""
			if b.config.RateLimitPrompt != "" {
				generatedText, err := b.llm.GenerateArbitraryResponse(b.config.RateLimitPrompt, "") // Контекст не нужен
				if err != nil {
					log.Printf("[ERROR] Чат %d: Ошибка генерации динамической части сообщения о лимите: %v", chatID, err)
					// В случае ошибки можно использовать пустую строку или запасной вариант
				} else {
					dynamicPart = strings.TrimSpace(generatedText)
				}
			}

			// --- Формирование и отправка итогового сообщения ---
			fullMessage := fmt.Sprintf("%s %s\nПодожди еще: %s",
				b.config.RateLimitStaticText,
				dynamicPart,
				formatRemainingTime(remainingTime),
			)

			if lastInfoMsgID != 0 {
				b.deleteMessage(chatID, lastInfoMsgID)
			}
			msg := tgbotapi.NewMessage(chatID, fullMessage)
			sentMsg, err := b.api.Send(msg)
			if err == nil {
				b.settingsMutex.Lock()
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
			chatID, lastReq, ok, durationSinceLast, summaryRequestInterval)

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
		instructionText := "📝 Введите данные профиля в следующем сообщении в формате:\\n`@никнейм - Прозвище - Пол (male/female/other) - Настоящее имя (если известно) - Био`\\n\\n_Пол, Наст\\.имя и Био могут быть пустыми или отсутствовать\\. Пол можно указать как m/f\\._\\n\\n_Это сообщение будет удалено через 15 секунд\\._"
		instructionMsg := tgbotapi.NewMessage(chatID, instructionText)
		instructionMsg.ParseMode = tgbotapi.ModeMarkdown // Используем стандартный Markdown

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

	// --- Admin Command: /backfill_embeddings ---
	case "backfill_embeddings":
		if !isUserAdmin {
			b.sendReply(chatID, "🚫 У вас нет прав для выполнения этой команды.")
			return
		}
		if b.config.StorageType != config.StorageTypeMongo {
			b.sendReply(chatID, "🚫 Команда доступна только при использовании MongoDB.")
			return
		}
		if !b.config.LongTermMemoryEnabled {
			b.sendReply(chatID, "🚫 Долгосрочная память (векторный поиск) выключена в настройках.")
			return
		}

		log.Printf("[ADMIN CMD] Пользователь %s (%d) инициировал команду /backfill_embeddings в чате %d.", username, userID, chatID)
		// Запускаем бэкфилл в отдельной горутине, чтобы не блокировать бота
		go b.runBackfillEmbeddings(chatID)
		// Используем новую функцию для удаления сообщения через 15 секунд
		go b.sendAndDeleteAfter(chatID, "⏳ Запускаю процесс заполнения векторных представлений для сообщений в этом чате. Это может занять много времени...", 15*time.Second)

	// --- Admin Command: /reset_autobio ---
	case "reset_autobio":
		if !isUserAdmin {
			b.sendReply(chatID, "🚫 У вас нет прав для выполнения этой команды.")
			return
		}
		if !b.config.AutoBioEnabled {
			b.sendReply(chatID, "🚫 Функция AutoBio отключена в конфигурации.")
			return
		}
		log.Printf("[ADMIN CMD] Пользователь %s (%d) инициировал команду /reset_autobio в чате %d.", username, userID, chatID)
		err := b.storage.ResetAutoBioTimestamps(chatID)
		if err != nil {
			log.Printf("[ERROR][CmdHandler /reset_autobio] Ошибка сброса времени AutoBio для чата %d: %v", chatID, err)
			b.sendReply(chatID, fmt.Sprintf("❌ Ошибка при сбросе времени AutoBio: %v", err)) // Ошибку оставляем
		} else {
			// Сообщение об успехе удаляем через 15 секунд
			go b.sendAndDeleteAfter(chatID, "✅ Время последнего анализа AutoBio сброшено для всех пользователей этого чата. Полный анализ будет выполнен при следующем запуске.", 15*time.Second)
		}

	// --- Admin Command: /trigger_autobio ---
	case "trigger_autobio":
		if !isUserAdmin {
			b.sendReply(chatID, "🚫 У вас нет прав для выполнения этой команды.")
			return
		}
		if !b.config.AutoBioEnabled {
			b.sendReply(chatID, "🚫 Функция AutoBio отключена в конфигурации.")
			return
		}
		log.Printf("[ADMIN CMD] Пользователь %s (%d) инициировал команду /trigger_autobio в чате %d.", username, userID, chatID)
		// Запускаем анализ для текущего чата в отдельной горутине
		go b.runAutoBioAnalysisForChat(chatID)
		// Сообщение о запуске удаляем через 15 секунд
		go b.sendAndDeleteAfter(chatID, "⏳ Запускаю анализ AutoBio для всех пользователей этого чата. Это может занять некоторое время...", 15*time.Second)

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
