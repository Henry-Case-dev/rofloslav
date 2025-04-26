package bot

import (
	"context"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleMessage обрабатывает обычные сообщения пользователей (не команды или коллбэки)
// Эта функция была значительно упрощена, основная логика вынесена.
func (b *Bot) handleMessage(update tgbotapi.Update) {
	startTime := time.Now()
	defer func() {
		log.Printf("[DEBUG][Timing] Обработка Message (ID: %d) заняла %s", update.Message.MessageID, time.Since(startTime))
	}()

	message := update.Message
	chatID := message.Chat.ID
	// username := "" // Удалена неиспользуемая переменная

	if b.config.Debug {
		log.Printf("[DEBUG][MH] Chat %d: Entering handleMessage for message ID %d.", chatID, message.MessageID)
	}

	var textMessage *tgbotapi.Message // Сообщение для обработки (оригинальное или транскрибированное)

	// === Обработка голосовых сообщений ===
	if message.Voice != nil {
		// Вызываем handleVoiceMessage, который теперь возвращает только error
		err := b.handleVoiceMessage(message)
		if err != nil {
			log.Printf("[ERROR][MH] Error handling voice message %d in chat %d: %v", message.MessageID, message.Chat.ID, err)
			// Ошибка уже должна быть отправлена пользователю и залогирована в handleVoiceMessage
			return // Прекращаем дальнейшую обработку
		}
		// Если ошибки нет, значит, сообщение успешно транскрибировано и ОТПРАВЛЕНО в чат.
		// Дальнейшая обработка этого сообщения (как текстового) здесь не нужна.
		// Однако, нам все еще нужно сохранить оригинальное сообщение в БД.
		// Поэтому мы НЕ выходим из handleMessage, а просто устанавливаем textMessage = message
		textMessage = message // Используем оригинальное сообщение для сохранения в БД
		log.Printf("[DEBUG][MH] Voice message %d processed and sent by handleVoiceMessage. Proceeding to save original message.", message.MessageID)
	} else if message.Photo != nil && len(message.Photo) > 0 {
		// === Обработка фотографий ===
		err := b.handlePhotoMessage(context.Background(), message)
		if err != nil {
			log.Printf("[ERROR][MH] Error handling photo message %d in chat %d: %v", message.MessageID, message.Chat.ID, err)
			// Ошибка уже должна быть отправлена пользователю в handlePhotoMessage
			return // Прекращаем дальнейшую обработку
		}
		// Аналогично голосовым сообщениям, сохраняем оригинальное сообщение
		textMessage = message
		log.Printf("[DEBUG][MH] Photo message %d processed and sent by handlePhotoMessage. Proceeding to save original message.", message.MessageID)
	} else {
		// Если это не голосовое и не фото, используем оригинальное сообщение
		textMessage = message
	}
	// === Конец обработки голосовых и фото ===

	// Теперь используем textMessage для дальнейшей обработки
	// (В случае голоса/фото это будет оригинальное сообщение для сохранения)
	if textMessage == nil {
		log.Printf("[ERROR][MH] textMessage is nil after voice/photo handling check for update %d", update.UpdateID)
		return
	}

	// Обновляем ключевые переменные на основе textMessage (оригинального сообщения)
	message = textMessage    // Используем textMessage как основное сообщение далее
	chatID = message.Chat.ID // Убедимся, что chatID актуален
	// username := message.From.UserName // Обновим username на всякий случай

	// === Settings Read Start ===
	b.settingsMutex.RLock() // Use RLock for reading settings
	settings, exists := b.chatSettings[chatID]
	localIsActive := exists && settings.Active
	localPendingSetting := ""
	localSrachEnabled := false
	localVoiceEnabled := b.config.VoiceTranscriptionEnabledDefault

	needsReset := false // Flag to indicate if PendingSetting needs reset later - Restored

	if exists {
		localPendingSetting = settings.PendingSetting

		// --- Читаем настройки из БД для Srach и Voice ---
		// Не разблокируем мьютекс настроек памяти здесь
		// Вместо этого прочитаем настройки БД внутри RLock
		dbSettings, errDb := b.storage.GetChatSettings(chatID)
		if errDb == nil && dbSettings != nil { // Успешно получили настройки из БД
			if dbSettings.SrachAnalysisEnabled != nil {
				localSrachEnabled = *dbSettings.SrachAnalysisEnabled
			}
			if dbSettings.VoiceTranscriptionEnabled != nil {
				localVoiceEnabled = *dbSettings.VoiceTranscriptionEnabled
			}
		} else { // Ошибка чтения или нет настроек в БД
			if errDb != nil {
				log.Printf("[ERROR][MH] Chat %d: Ошибка получения настроек из DB внутри RLock: %v. Используем дефолты.", chatID, errDb)
			} else {
				// Настроек нет, используем дефолты (уже установлены)
			}
			localSrachEnabled = b.config.SrachAnalysisEnabled
			localVoiceEnabled = b.config.VoiceTranscriptionEnabledDefault
		}
		// --- Конец чтения Srach/Voice из БД ---

		// Determine if we need to reset PendingSetting based on its current value
		if localPendingSetting != "" {
			needsReset = true
			log.Printf("[DEBUG][MH Pending Check] Чат %d: Обнаружен PendingSetting '%s'. Установлен флаг needsReset.", chatID, localPendingSetting)
			// No need to reset here, just set the flag
		}

	} else {
		// Settings don't exist for this chat yet (should have been created by ensureChatInitialized)
		// We can potentially log a warning here if this state is unexpected
		log.Printf("[WARN][MH] Chat %d: Настройки чата не найдены во время чтения в handleMessage. Используем дефолты.", chatID)
		localSrachEnabled = b.config.SrachAnalysisEnabled
		localVoiceEnabled = b.config.VoiceTranscriptionEnabledDefault
	}
	// Single RUnlock after all necessary values are read
	b.settingsMutex.RUnlock() // (Unlock 1a)
	// === Settings Read Complete, Lock Released ===

	if b.config.Debug {
		log.Printf("[DEBUG][MH] Chat %d: Read settings (Active: %t, Pending: '%s', Srach: %t, Voice: %t). Lock released.",
			chatID, localIsActive, localPendingSetting, localSrachEnabled, localVoiceEnabled)
	}

	// Если сообщение пришло от пользователя и ожидается ввод для настройки
	var pendingSettingKey string
	b.settingsMutex.RLock()
	if settings, exists := b.chatSettings[chatID]; exists {
		pendingSettingKey = settings.PendingSetting
	}
	b.settingsMutex.RUnlock()

	if pendingSettingKey != "" && message.Text != "" && !strings.HasPrefix(message.Text, "/") && b.isAdmin(message.From) {
		if b.config.Debug {
			log.Printf("[DEBUG][MH] Chat %d User %d (%s): Обнаружен ожидаемый ввод для ключа '%s'. Текст: '%s'. Вызов handlePendingSettingInput...", chatID, message.From.ID, message.From.UserName, pendingSettingKey, message.Text)
		}
		// Вызываем обработчик из input_handler.go (или settings_input_handler.go)
		err := b.handlePendingSettingInput(chatID, message.From.ID, message.From.UserName, pendingSettingKey, message.Text, message.MessageID)
		if err != nil {
			log.Printf("[WARN][MH] Chat %d User %d: Ошибка обработки ожидаемого ввода для '%s': %v", chatID, message.From.ID, pendingSettingKey, err)
			// Не выходим, возможно, нужно обработать как обычное сообщение?
		} else {
			// Если ввод успешно обработан, выходим
			if b.config.Debug {
				log.Printf("[DEBUG][MH] Chat %d: Ожидаемый ввод для '%s' успешно обработан. Прерываем дальнейшую обработку сообщения.", chatID, pendingSettingKey)
			}
			return
		}
	}

	// Continue normal logic if no pending setting was handled

	// --- Check Activity ---
	if !localIsActive { // Use local variable read earlier
		if b.config.Debug {
			log.Printf("[DEBUG][MH] Chat %d: Bot is inactive. Exiting handleMessage.", chatID)
		}
		return // If bot is inactive, exit
	}

	// Добавляем сообщение в хранилище
	// Этот блок теперь выполняется всегда, так как AddMessage удален из handleVoiceMessage
	b.storage.AddMessage(chatID, message) // Добавляем textMessage (оригинальное)
	if b.config.Debug {
		log.Printf("[DEBUG][MH] Chat %d: Message ID %d (IsVoice: %t) added/updated in storage.", chatID, message.MessageID, message.Voice != nil)
	}

	// --- Srach Analysis ---
	// Используем message (текст из голоса или оригинальный)
	if localSrachEnabled { // Используем локальную переменную
		// b.handleSrachLogic(messageToProcess) // TODO: Восстановить или удалить эту логику
		if b.config.Debug {
			log.Printf("[DEBUG][MH] Chat %d: Srach logic skipped (commented out).", chatID) // Изменено сообщение лога
		}
		// НЕ ВЫХОДИМ здесь, чтобы сообщение могло вызвать и обычный ответ
	} else {
		if b.config.Debug {
			log.Printf("[DEBUG][MH] Chat %d: Srach analysis disabled.", chatID)
		}
	}
	// --- End Srach Analysis ---

	// === Дальнейшая логика использует message ===

	// --- Check for Direct Reply / Mention ---
	if b.config.Debug {
		log.Printf("[DEBUG][MH] Chat %d: Checking for reply to bot or mention.", chatID)
	}
	// Используем message для проверки
	isReplyToBot := message.ReplyToMessage != nil && message.ReplyToMessage.From != nil && message.ReplyToMessage.From.ID == b.api.Self.ID
	mentionsBot := message.Entities != nil && func() bool {
		for _, entity := range message.Entities {
			if entity.Type == "mention" {
				mention := message.Text[entity.Offset : entity.Offset+entity.Length]
				if mention == "@"+b.api.Self.UserName {
					return true
				}
			}
		}
		return false
	}()
	if isReplyToBot || mentionsBot {
		if b.config.Debug {
			log.Printf("[DEBUG][MH] Chat %d: IsReplyToBot: %t, MentionsBot: %t. Checking direct reply limit.", chatID, isReplyToBot, mentionsBot)
		}
		// Проверяем лимит прямых ответов
		limitEnabled, _, _ := b.getDirectReplyLimitSettings(chatID) // Используем _ для неиспользуемых count и duration
		if limitEnabled {
			if !b.checkDirectReplyLimit(chatID, message.From.ID) { // ИНВЕРТИРОВАНО: ! checkDirectReplyLimit возвращает false, если лимит превышен
				if b.config.Debug {
					log.Printf("[DEBUG][MH] Chat %d: Direct reply limit EXCEEDED.", chatID)
				}
				b.sendDirectLimitExceededReply(chatID, message.MessageID)
				// Выход после обработки прямого ответа (лимит превышен)
				log.Printf("[DEBUG][MH EXIT POINT] Chat %d: Reached EXIT point after direct reply/mention (limit exceeded).", chatID)
				return
			} else {
				// Лимит не превышен, продолжаем с прямым ответом
				if b.config.Debug {
					log.Printf("[DEBUG][MH] Chat %d: Direct reply limit NOT exceeded. Proceeding with direct response.", chatID)
				}
			}
		}

		// Если лимит выключен ИЛИ не превышен - отправляем прямой ответ
		b.sendDirectResponse(chatID, message)
		// Выход после обработки прямого ответа
		log.Printf("[DEBUG][MH EXIT POINT] Chat %d: Reached EXIT point after direct reply/mention (sent direct response).", chatID)
		return
	}

	// --- Check AI Response Condition ---
	if b.config.Debug {
		log.Printf("[DEBUG][MH] Chat %d: No direct mention. Checking conditions for AI response.", chatID)
	}
	b.settingsMutex.Lock()
	settings, exists = b.chatSettings[chatID] // Перепроверяем settings под мьютексом
	shouldReply := false
	if exists && settings.Active {
		settings.MessageCount++
		log.Printf("[DEBUG][MH] Chat %d: Message count incremented to %d.", chatID, settings.MessageCount)
		targetMessages := settings.MinMessages + b.randSource.Intn(settings.MaxMessages-settings.MinMessages+1)
		if settings.MessageCount >= targetMessages {
			shouldReply = true
			settings.MessageCount = 0                  // Сбрасываем счетчик
			settings.LastMessageID = message.MessageID // Сохраняем ID сообщения, вызвавшего ответ
			log.Printf("[DEBUG][MH] Chat %d: AI reply condition met (Count=%d >= Target=%d). Resetting count. LastMessageID set to %d.", chatID, settings.MessageCount+targetMessages /*исходное знач.*/, targetMessages, settings.LastMessageID)
		} else {
			log.Printf("[DEBUG][MH] Chat %d: Checking AI reply condition: Count(%d) >= Target(%d)? -> false", chatID, settings.MessageCount, targetMessages)
		}
	}
	b.settingsMutex.Unlock()
	log.Printf("[DEBUG][MH] Chat %d: Settings mutex unlocked after AI response check. ShouldReply: %t.", chatID, shouldReply)

	if shouldReply {
		// Запускаем генерацию ответа AI в отдельной горутине
		go b.sendAIResponse(chatID)
	}

	if b.config.Debug {
		log.Printf("[DEBUG][MH] Chat %d: Exiting handleMessage normally.", chatID)
	}

	// --- Сброс PendingSetting, если он был установлен и обработан (или просто обнаружен) ---
	if needsReset {
		b.settingsMutex.Lock()
		if settings, exists := b.chatSettings[chatID]; exists {
			if settings.PendingSetting != "" { // Дополнительная проверка, чтобы не логировать лишний раз
				log.Printf("[DEBUG][MH Pending Reset] Чат %d: Сброс PendingSetting (был '%s').", chatID, settings.PendingSetting)
				settings.PendingSetting = ""
				// settings.PendingSettingUserID = 0 // Поле удалено
			}
		}
		b.settingsMutex.Unlock()
	}

	// --- Обновление профиля пользователя ---
	// Вынесено в конец, чтобы не мешать основной логике
	if message.From != nil {
		// Функция updateUserProfileIfNeeded была перемещена в profile_handler.go
		// Оставляем вызов здесь, он будет исправлен позже при модификации структуры Bot
		go b.updateUserProfileIfNeeded(chatID, message.From, message.Date) // Передаем оригинальное время сообщения
	}
}

// Commenting out moved function sendReplyAndDeleteAfter
/*
func (b *Bot) sendReplyAndDeleteAfter(chatID int64, text string, delay time.Duration) (*tgbotapi.Message, error) {
	// ... implementation ...
}
*/

// --- Неиспользуемая функция downloadFile удалена --- //

/*
// checkDirectReplyLimit была перемещена в responder.go
func (b *Bot) checkDirectReplyLimit(chatID int64, userID int64) bool {
	// ... (код перемещен)
}
*/

/*
// sendDirectLimitExceededReply была перемещена в responder.go
func (b *Bot) sendDirectLimitExceededReply(chatID int64, replyToMessageID int) {
	// ... (код перемещен)
}
*/

/*
// updateUserProfileIfNeeded была перемещена в profile_handler.go
func (b *Bot) updateUserProfileIfNeeded(chatID int64, user *tgbotapi.User, messageDate int) {
    // ... (код удален)
}
*/
