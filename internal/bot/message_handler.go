package bot

import (
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/storage"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleMessage processes regular user messages (not commands or callbacks)
func (b *Bot) handleMessage(update tgbotapi.Update) {
	startTime := time.Now()
	message := update.Message
	chatID := message.Chat.ID
	username := message.From.UserName

	if b.config.Debug {
		log.Printf("[DEBUG][MH] Chat %d: Entering handleMessage for message ID %d.", chatID, message.MessageID)
	}

	// --- НАЧАЛО НОВОЙ ЛОГИКИ ГОЛОСОВЫХ ---
	var originalMessage *tgbotapi.Message = update.Message
	var textMessage *tgbotapi.Message // Для создания текстового представления

	if update.Message != nil && update.Message.Voice != nil {
		log.Printf("[DEBUG][VoiceHandler] Chat %d: Получено голосовое сообщение ID %d (FileID: %s, Duration: %ds)", chatID, originalMessage.MessageID, originalMessage.Voice.FileID, originalMessage.Voice.Duration)

		// 1. Получаем URL для скачивания файла
		fileURL, err := b.api.GetFileDirectURL(originalMessage.Voice.FileID)
		if err != nil {
			log.Printf("[ERROR][VoiceHandler] Chat %d: Ошибка получения URL файла: %v", chatID, err)
			b.sendReply(chatID, "⚠️ Не удалось получить ссылку на аудиофайл.")
			return // Прерываем обработку этого сообщения
		}

		// 2. Скачиваем файл
		loadingMsg, _ := b.sendReplyAndDeleteAfter(chatID, "⏳ Скачиваю и распознаю голосовое...", 0) // 0 - не удалять пока
		audioData, err := downloadFile(fileURL)
		if err != nil {
			log.Printf("[ERROR][VoiceHandler] Chat %d: Ошибка скачивания файла: %v", chatID, err)
			b.sendReply(chatID, "⚠️ Не удалось скачать аудиофайл.")
			if loadingMsg != nil {
				b.deleteMessage(chatID, loadingMsg.MessageID)
			}
			return
		}

		// 3. Транскрибируем аудио
		// Используем MIME-тип из Voice, если он есть, иначе предполагаем 'audio/ogg'
		mimeType := originalMessage.Voice.MimeType
		if mimeType == "" {
			mimeType = "audio/ogg" // Типичный формат для голосовых Telegram
		}
		rawTranscript, err := b.llm.TranscribeAudio(audioData, mimeType)
		if err != nil {
			log.Printf("[ERROR][VoiceHandler] Chat %d: Ошибка транскрибации: %v", chatID, err)
			b.sendReply(chatID, "⚠️ Не удалось распознать речь в сообщении.")
			if loadingMsg != nil {
				b.deleteMessage(chatID, loadingMsg.MessageID)
			}
			return
		}

		if rawTranscript == "" {
			log.Printf("[WARN][VoiceHandler] Chat %d: Транскрипция вернула пустой текст.", chatID)
			b.sendReply(chatID, "⚠️ Распознанный текст пуст.")
			if loadingMsg != nil {
				b.deleteMessage(chatID, loadingMsg.MessageID)
			}
			return
		}

		// 4. Форматируем текст (пунктуация, абзацы)
		formattedText, err := b.llm.GenerateArbitraryResponse(b.config.VoiceFormatPrompt, rawTranscript)
		if err != nil {
			log.Printf("[WARN][VoiceHandler] Chat %d: Ошибка форматирования текста: %v. Использую сырой текст.", chatID, err)
			formattedText = rawTranscript // Используем сырой текст как fallback
		}

		// 5. Создаем представительное текстовое сообщение
		textMessage = &tgbotapi.Message{
			MessageID:   originalMessage.MessageID,
			From:        originalMessage.From,
			SenderChat:  originalMessage.SenderChat,
			Date:        originalMessage.Date,
			Chat:        originalMessage.Chat,
			ForwardFrom: originalMessage.ForwardFrom, // Сохраняем информацию о пересылке
			// ... другие поля по необходимости ...
			ReplyToMessage: originalMessage.ReplyToMessage,
			Text:           formattedText, // Вставляем отформатированный текст
			// Оставляем Entities пустым, т.к. мы их не генерировали
			// Voice поле здесь не нужно, т.к. это текстовое представление
		}

		// 6. Удаляем сообщение "Скачиваю..."
		if loadingMsg != nil {
			b.deleteMessage(chatID, loadingMsg.MessageID)
		}
		log.Printf("[DEBUG][VoiceHandler] Chat %d: Голосовое сообщение ID %d обработано. Текст: %s...", chatID, originalMessage.MessageID, truncateString(formattedText, 50))

		// --- ОТПРАВКА РАСПОЗНАННОГО ТЕКСТА ---
		// Проверяем, включена ли отправка транскрипции в настройках чата
		dbSettings, errSettings := b.storage.GetChatSettings(chatID)
		sendTranscription := b.config.VoiceTranscriptionEnabledDefault // Значение по умолчанию
		if errSettings == nil && dbSettings != nil && dbSettings.VoiceTranscriptionEnabled != nil {
			sendTranscription = *dbSettings.VoiceTranscriptionEnabled // Используем значение из БД, если оно есть
		} else if errSettings != nil {
			log.Printf("[WARN][VoiceHandler] Chat %d: Ошибка получения настроек чата для проверки VoiceTranscriptionEnabled: %v. Используется дефолтное значение (%t).", chatID, errSettings, sendTranscription)
		}

		if sendTranscription {
			if formattedText != "" { // Убедимся, что текст не пустой
				// Форматируем текст ответа
				// Экранируем сам текст перед вставкой в MarkdownV2
				escapedFormattedText := escapeMarkdownV2(formattedText)
				// Используем одинарные подчеркивания для курсива в MarkdownV2
				finalReplyText := fmt.Sprintf("🎤 Перевожу голосовуху:\n_%s_", escapedFormattedText)
				replyMsg := tgbotapi.NewMessage(chatID, finalReplyText)
				replyMsg.ReplyToMessageID = message.MessageID // Устанавливаем ReplyTo
				replyMsg.ParseMode = "MarkdownV2"             // Используем MarkdownV2
				_, replyErr := b.api.Send(replyMsg)
				if replyErr != nil {
					log.Printf("[ERROR][VoiceHandler] Чат %d: Ошибка отправки транскрибированного текста: %v", chatID, replyErr)
				}
			}
		} else {
			if b.config.Debug {
				log.Printf("[DEBUG][VoiceHandler] Chat %d: Отправка транскрипции отключена в настройках.", chatID)
			}
		}
		// --- КОНЕЦ ОТПРАВКИ ---

	} else {
		// Если это не голосовое, используем оригинальное сообщение
		textMessage = originalMessage
	}

	// --- КОНЕЦ НОВОЙ ЛОГИКИ ГОЛОСОВЫХ ---

	// Теперь используем textMessage (оригинальное или созданное из аудио) для дальнейшей обработки
	if textMessage == nil {
		// Это не должно происходить, но на всякий случай
		log.Printf("[ERROR][MH] textMessage is nil after voice handling for update %d", update.UpdateID)
		return
	}

	message = textMessage    // Используем переменную message далее
	chatID = message.Chat.ID // Убедимся, что chatID актуален

	// === Read Settings with Minimized Lock Duration ===
	b.settingsMutex.Lock() // Lock 1 (Write lock)
	settings, exists := b.chatSettings[chatID]
	if !exists {
		log.Printf("[DEBUG][MH] Chat %d: Settings not found. Initializing.", chatID)
		b.settingsMutex.Unlock() // Unlock 1a (before calling func)

		// Initialize settings outside the main lock
		_, initialized := b.ensureChatInitializedAndWelcome(update) // This locks/unlocks internally
		if !initialized {
			// If ensureChat didn't initialize (e.g., settings magically appeared concurrently?), try refetching
			log.Printf("[DEBUG][MH] Chat %d: ensureChatInitializedAndWelcome reported no new initialization. Refetching settings.", chatID)
		}

		// Re-lock to get the definitive settings pointer
		b.settingsMutex.Lock()                    // Lock 2 (Write lock after func/refetch)
		settings, exists = b.chatSettings[chatID] // Refetch settings
		if !exists || settings == nil {           // Check if initialization failed critically
			log.Printf("[FATAL][MH] Chat %d: Failed to get valid settings even after initialization attempt.", chatID)
			b.settingsMutex.Unlock() // Unlock 2a (fatal exit)
			return
		}
		log.Printf("[DEBUG][MH] Chat %d: Settings obtained after initialization attempt.", chatID)
	}

	// Read all needed settings into local variables *under the lock*
	localPendingSetting := settings.PendingSetting
	localLastInfoMsgID := settings.LastInfoMessageID
	localIsActive := settings.Active
	localSrachEnabled := settings.SrachAnalysisEnabled
	localMinMessages := settings.MinMessages // Needed for AI response check later
	localMaxMessages := settings.MaxMessages // Needed for AI response check later

	// Reset pending state if necessary *before* unlocking
	needsReset := localPendingSetting != ""
	if needsReset {
		settings.PendingSetting = ""
		settings.LastInfoMessageID = 0
		if b.config.Debug {
			log.Printf("[DEBUG][MH] Chat %d: Resetting pending setting '%s' under lock.", chatID, localPendingSetting)
		}
	}

	b.settingsMutex.Unlock() // Single Unlock Point (Unlock 1b or 2b)
	// === Settings Read Complete, Lock Released ===

	if b.config.Debug {
		log.Printf("[DEBUG][MH] Chat %d: Read settings (Active: %t, Pending: '%s', Srach: %t). Lock released.",
			chatID, localIsActive, localPendingSetting, localSrachEnabled)
	}

	// --- Handle Pending Settings Input ---
	if localPendingSetting != "" { // <--- This condition must be TRUE for the log to appear
		log.Printf("[DEBUG][MH Pending Check] Chat %d: Entered 'if localPendingSetting != \"\"'. Value: '%s'", chatID, localPendingSetting) // ADDED LOG

		if needsReset { // Use the boolean flag derived from localPendingSetting
			if b.config.Debug {
				log.Printf("[DEBUG][MH] Chat %d: Handling pending setting input for '%s'.", chatID, localPendingSetting)
			}
			// Delete prompt message (uses localLastInfoMsgID read earlier)
			if localLastInfoMsgID != 0 {
				b.deleteMessage(chatID, localLastInfoMsgID)
			}
			// Delete user input message
			b.deleteMessage(chatID, message.MessageID)

			if localPendingSetting == "profile_data" {
				// --- Handle 'profile_data' input ---
				b.deleteMessage(chatID, message.MessageID)

				log.Printf("[DEBUG][MH Profile Data] Чат %d: Обработка ввода профиля: %s", chatID, message.Text)
				targetUsername, _, alias, gender, realName, bio, parseErr := parseProfileArgs(message.Text)
				if parseErr != nil {
					log.Printf("[ERROR][MH Profile Data] Чат %d: Ошибка парсинга данных профиля '%s': %v", chatID, message.Text, parseErr)
					b.sendReply(chatID, fmt.Sprintf("🚫 Ошибка парсинга: %v\nПопробуйте еще раз или введите /cancel", parseErr))
					// Оставляем PendingSetting = "profile_data", чтобы пользователь мог попробовать еще раз
					b.settingsMutex.Unlock() // Разблокируем перед выходом
					return                   // Выходим, чтобы пользователь попробовал снова
				}

				log.Printf("[DEBUG][MH Profile Data] Чат %d: Распарсено: User=%s, Alias=%s, Gender=%s, RealName=%s, Bio=%s",
					chatID, targetUsername, alias, gender, realName, bio)

				// Пытаемся найти существующий профиль по username
				existingProfile, findErr := b.findUserProfileByUsername(chatID, targetUsername)
				if findErr != nil {
					log.Printf("[ERROR][MH Profile Data] Чат %d: Ошибка поиска профиля по username '%s': %v", chatID, targetUsername, findErr)
					b.sendReply(chatID, "🚫 Произошла ошибка при поиске существующего профиля. Попробуйте позже.")
					settings.PendingSetting = "" // Сбрасываем ожидание
					b.settingsMutex.Unlock()     // Разблокируем перед выходом
					return
				}

				var profileToSave storage.UserProfile
				if existingProfile != nil {
					log.Printf("[DEBUG][MH Profile Data] Чат %d: Найден существующий профиль для @%s (UserID: %d). Обновляем.", chatID, targetUsername, existingProfile.UserID)
					profileToSave = *existingProfile // Копируем существующий
					// Обновляем только те поля, которые были введены
					profileToSave.Alias = alias       // Всегда обновляем Alias
					profileToSave.Gender = gender     // Всегда обновляем Gender
					profileToSave.RealName = realName // Всегда обновляем RealName
					profileToSave.Bio = bio           // Всегда обновляем Bio
				} else {
					log.Printf("[DEBUG][MH Profile Data] Чат %d: Профиль для @%s не найден. Создаем новый.", chatID, targetUsername)
					// Пытаемся получить ID пользователя по username (может быть неточным, если пользователя нет в чате)
					foundUserID, _ := b.getUserIDByUsername(chatID, targetUsername)
					if foundUserID == 0 {
						log.Printf("[WARN][MH Profile Data] Чат %d: Не удалось определить UserID для @%s. Профиль будет создан без UserID.", chatID, targetUsername)
					}
					profileToSave = storage.UserProfile{
						ChatID:   chatID,
						UserID:   foundUserID, // Может быть 0
						Username: targetUsername,
						Alias:    alias,
						Gender:   gender,
						RealName: realName,
						Bio:      bio,
					}
				}

				// Устанавливаем время последнего обновления
				profileToSave.LastSeen = time.Now() // Используем текущее время как LastSeen при обновлении профиля

				// Сохраняем профиль
				if saveErr := b.storage.SetUserProfile(&profileToSave); saveErr != nil {
					log.Printf("[ERROR][MH Profile Data] Чат %d: Ошибка сохранения профиля для @%s: %v", chatID, targetUsername, saveErr)
					b.sendReply(chatID, "🚫 Произошла ошибка при сохранении профиля.")
				} else {
					log.Printf("[INFO][MH Profile Data] Чат %d: Профиль для @%s успешно сохранен/обновлен.", chatID, targetUsername)
					b.sendReply(chatID, fmt.Sprintf("✅ Профиль для @%s успешно сохранен/обновлен.", targetUsername))
				}

				// Сбрасываем ожидание ввода
				settings.PendingSetting = ""
				b.settingsMutex.Unlock() // Разблокируем после обработки

				// Удаляем сообщение с введенными данными и сообщение-инструкцию
				b.deleteMessage(chatID, message.MessageID)
				if settings.LastInfoMessageID != 0 {
					b.deleteMessage(chatID, settings.LastInfoMessageID)
					// Можно сбросить LastInfoMessageID в настройках после удаления, если нужно
					// settings.LastInfoMessageID = 0 // Сброс ID инструкции
				}

				return // Завершаем обработку этого сообщения
			} // --- End Handle 'profile_data' ---

			// --- НОВЫЙ БЛОК: Обработка остальных PendingSettings и /cancel ---
			// Сначала проверим на команду /cancel
			if message.Text == "/cancel" {
				log.Printf("[DEBUG][MH] Chat %d: Пользователь %s отменил ввод настройки '%s'.", chatID, username, localPendingSetting)
				b.settingsMutex.Lock()
				if settings, exists := b.chatSettings[chatID]; exists {
					settings.PendingSetting = "" // Сбрасываем ожидание
					// Удаляем сообщение с запросом ввода, если оно было
					if settings.LastInfoMessageID != 0 {
						b.deleteMessage(chatID, settings.LastInfoMessageID)
						settings.LastInfoMessageID = 0 // Сбрасываем ID
					}
				}
				b.settingsMutex.Unlock()
				b.deleteMessage(chatID, message.MessageID)                        // Удаляем сообщение /cancel
				b.sendReplyAndDeleteAfter(chatID, "Ввод отменен.", 5*time.Second) // Отправляем подтверждение и удаляем через 5 сек
				// Возвращаем в главное меню (удаляя старое сообщение с промптом, если оно еще не удалено)
				// lastInfoMsgID уже сброшен, так что передаем 0
				b.sendMainMenu(chatID, 0)
				return
			}

			// Handle pending setting input
			log.Printf("[DEBUG][MH Pending] Chat %d: Handling pending setting '%s' with input: %s", chatID, localPendingSetting, message.Text)

			// Attempt to convert the input text to a number
			valueInt, err := strconv.Atoi(message.Text)
			if err != nil {
				log.Printf("[WARN][MH Pending] Chat %d: Invalid input for %s: '%s' - not a number. Error: %v", chatID, localPendingSetting, message.Text, err)
				b.sendReply(chatID, "🚫 Введите числовое значение.")
				// Don't clear pending state here, let the user try again or cancel
				return
			}

			// Define context for storage operations
			// ctx := context.Background() // Or context.TODO() or pass from higher level

			switch localPendingSetting {
			case "direct_limit_count":
				if valueInt >= 0 { // 0 means unlimited
					err = b.storage.UpdateDirectLimitCount(chatID, valueInt)
					if err != nil {
						log.Printf("[ERROR][MH Pending] User %d: Failed to save direct_limit_count %d: %v", chatID, valueInt, err)
						b.sendReply(chatID, "🚫 Произошла ошибка при сохранении настройки лимита сообщений.")
					} else {
						log.Printf("User %d: Лимит прямых сообщений установлен: %d", chatID, valueInt)
						b.sendReply(chatID, fmt.Sprintf("✅ Лимит прямых сообщений установлен: %d", valueInt))
						// Optionally, update and resend the settings keyboard if applicable
						// go b.sendSettingsKeyboard(chatID, 0) // Example if settings keyboard needs update
					}
					delete(b.pendingSettings, chatID) // Clear pending state after attempt (success or handled error)
				} else {
					b.sendReply(chatID, "🚫 Ошибка: Количество сообщений должно быть 0 или больше.")
				}

			case "direct_limit_duration":
				if valueInt > 0 { // Duration must be positive
					duration := time.Duration(valueInt) * time.Minute
					err = b.storage.UpdateDirectLimitDuration(chatID, duration)
					if err != nil {
						log.Printf("[ERROR][MH Pending] User %d: Failed to save direct_limit_duration %d mins: %v", chatID, valueInt, err)
						b.sendReply(chatID, "🚫 Произошла ошибка при сохранении настройки периода лимита.")
					} else {
						log.Printf("User %d: Период лимита прямых сообщений установлен: %d минут", chatID, valueInt)
						b.sendReply(chatID, fmt.Sprintf("✅ Период лимита прямых сообщений установлен: %d минут", valueInt))
						// Optionally, update and resend the settings keyboard if applicable
						// go b.sendSettingsKeyboard(chatID, 0) // Example if settings keyboard needs update
					}
					delete(b.pendingSettings, chatID) // Clear pending state after attempt
				} else {
					b.sendReply(chatID, "🚫 Ошибка: Период должен быть больше 0 минут.")
				}

			default:
				log.Printf("[WARN][MH Pending] Chat %d: Received input '%s' for unknown or unhandled pending setting '%s'", chatID, message.Text, localPendingSetting)
				delete(b.pendingSettings, chatID) // Clear unknown pending state
			}

			// --- Постобработка после switch ---
			// Удаляем сообщение пользователя
			b.deleteMessage(chatID, message.MessageID)

			// Удаляем сообщение с запросом ввода (если настройка была обновлена или произошла ошибка)
			var lastInfoMsgIDToDelete int
			b.settingsMutex.Lock()
			if settings, exists := b.chatSettings[chatID]; exists {
				// Сохраняем ID перед сбросом, чтобы удалить сообщение вне зависимости от успеха обновления
				lastInfoMsgIDToDelete = settings.LastInfoMessageID
				if strings.HasPrefix(localPendingSetting, "❌") {
					// Сбрасываем PendingSetting только если ввод завершен (успешно или с ошибкой, кроме случая запроса max_messages)
					if settings.PendingSetting != "max_messages" {
						settings.PendingSetting = ""
					}
					settings.LastInfoMessageID = 0 // Сбрасываем ID после использования
				}
			}
			b.settingsMutex.Unlock()

			if lastInfoMsgIDToDelete != 0 {
				b.deleteMessage(chatID, lastInfoMsgIDToDelete)
			}

			// Отправляем подтверждение или сообщение об ошибке
			b.sendReplyAndDeleteAfter(chatID, localPendingSetting, 10*time.Second) // Удаляем через 10 секунд

			// Если настройка была успешно обновлена И ввод завершен (не ждем max_messages),
			// возвращаемся к клавиатуре настроек.
			b.settingsMutex.RLock()
			pendingSettingAfterUpdate := ""
			if settings, exists := b.chatSettings[chatID]; exists {
				pendingSettingAfterUpdate = settings.PendingSetting
			}
			b.settingsMutex.RUnlock()

			if strings.HasPrefix(localPendingSetting, "❌") && pendingSettingAfterUpdate == "" {
				// Отправляем обновленную клавиатуру настроек (удаляя старое инфо-сообщение, если оно было)
				b.sendSettingsKeyboard(chatID, 0) // 0, т.к. инфо-сообщение уже удалено
			}

			return // Выходим из handleMessage после обработки ввода настройки
			// --- Конец НОВОГО БЛОКА ---

		} else {
			// Handle other pending settings (min/max messages, times, etc.)
			// Logic for these seems to be missing here. Add it if needed.
			log.Printf("[WARN][MH] Chat %d: Получено сообщение '%s' при ожидании настройки '%s', но обработчик не найден.", chatID, message.Text, localPendingSetting)
			b.sendReply(chatID, fmt.Sprintf("Получено '%s', но я ожидал значение для '%s'. Настройка не изменена. Используйте /settings для повтора.", message.Text, localPendingSetting))
			return // Input received but couldn't be processed
		}
	} else {
		// Continue normal logic if no pending setting was handled

		// --- Check Activity ---
		if !localIsActive { // Use local variable read earlier
			if b.config.Debug {
				log.Printf("[DEBUG][MH] Chat %d: Bot is inactive. Exiting handleMessage.", chatID)
			}
			return // If bot is inactive, exit
		}

		// Добавляем ОРИГИНАЛЬНОЕ сообщение в хранилище, чтобы сохранить метаданные (включая Voice для флага)
		// а textMessage (с распознанным текстом) используем для дальнейшей обработки
		if originalMessage != nil {
			b.storage.AddMessage(originalMessage.Chat.ID, originalMessage)
			if b.config.Debug {
				log.Printf("[DEBUG][MH] Chat %d: Original Message ID %d added to storage.", originalMessage.Chat.ID, originalMessage.MessageID)
			}
		} else {
			log.Printf("[WARN][MH] Chat %d: originalMessage is nil, cannot add to storage.", chatID)
		}

		/*
			// Обновляем профиль пользователя (используем From из textMessage/originalMessage)
			// КОММЕНТИРУЕМ ЭТОТ БЛОК, ЧТОБЫ ЗАПРЕТИТЬ АВТОМАТИЧЕСКОЕ ОБНОВЛЕНИЕ ПРОФИЛЕЙ
			if message.From != nil {
				go func(chatID int64, user *tgbotapi.User) {
					// Получаем текущий профиль (если есть) или создаем новый
					profile, err := b.storage.GetUserProfile(chatID, user.ID)
					if err != nil {
						log.Printf("[ERROR][UpdateProfile] Chat %d, User %d: Ошибка получения профиля: %v", chatID, user.ID, err)
						return // Не удалось получить, не обновляем
					}
					if profile == nil {
						profile = &storage.UserProfile{
							ChatID: chatID,
							UserID: user.ID,
						}
					}
					// Обновляем данные
					profile.Username = user.UserName
					profile.LastSeen = time.Unix(int64(message.Date), 0)
					// Устанавливаем Alias из FirstName при первом создании, если Alias пуст
					if profile.Alias == "" && user.FirstName != "" {
						profile.Alias = user.FirstName
					}
					// Сохраняем
					err = b.storage.SetUserProfile(profile)
					if err != nil {
						log.Printf("[ERROR][UpdateProfile] Chat %d, User %d: Ошибка сохранения профиля: %v", chatID, user.ID, err)
					}
				}(message.Chat.ID, message.From) // Передаем chatID и user в горутину
			}
		*/

		// --- Srach Analysis ---
		srachHandled := false  // Flag that message was handled by srach logic
		if localSrachEnabled { // Use local variable read earlier
			if b.config.Debug {
				log.Printf("[DEBUG][MH] Chat %d: Srach analysis enabled: true.", chatID)
			}
			isTrigger := b.isPotentialSrachTrigger(message)
			if b.config.Debug {
				log.Printf("[DEBUG][MH] Chat %d: Is potential srach trigger: %t.", chatID, isTrigger)
			}

			// Lock *only* for reading/modifying srach state
			b.settingsMutex.Lock()                                  // Lock 4 (Write lock for Srach logic)
			if settings, exists := b.chatSettings[chatID]; exists { // Re-fetch settings under lock
				if b.config.Debug {
					log.Printf("[DEBUG][MH] Chat %d: Settings mutex locked for Srach logic.", chatID)
				}
				currentSrachState := settings.SrachState
				// Copy slice header for modification (if needed, be careful with append)
				// srachMessages := settings.SrachMessages

				if currentSrachState == "none" && isTrigger {
					// Start of srach
					if b.config.Debug {
						log.Printf("[DEBUG][MH] Chat %d: Srach detected! State changing to 'detected'.", chatID)
					}
					settings.SrachState = "detected"
					settings.SrachStartTime = time.Now()
					settings.LastSrachTriggerTime = time.Now()
					settings.SrachMessages = []string{fmt.Sprintf("[%s] %s: %s", message.Time().Format("15:04"), username, message.Text)} // Start collecting messages
					settings.SrachLlmCheckCounter = 0
					srachHandled = true        // Mark as handled
					b.settingsMutex.Unlock()   // Unlock 4a (before sending warning)
					b.sendSrachWarning(chatID) // Send warning outside lock

				} else if currentSrachState == "detected" {
					// Srach already in progress
					if b.config.Debug {
						log.Printf("[DEBUG][MH] Chat %d: Srach already in progress. Adding message.", chatID)
					}
					// Append message - make sure to assign back to settings.SrachMessages
					settings.SrachMessages = append(settings.SrachMessages, fmt.Sprintf("[%s] %s: %s", message.Time().Format("15:04"), username, message.Text))
					settings.LastSrachTriggerTime = time.Now() // Update last trigger time
					srachHandled = true                        // Mark as handled
					b.settingsMutex.Unlock()                   // Unlock 4b

				} else {
					// State "none" and not a trigger, or "analyzing"
					b.settingsMutex.Unlock() // Unlock 4c (no change)
					if b.config.Debug {
						log.Printf("[DEBUG][MH] Chat %d: Settings mutex unlocked (no active srach, not a trigger or analyzing).", chatID)
					}
				}
			} else {
				// Settings disappeared? Should not happen.
				log.Printf("[ERROR][MH] Chat %d: Settings disappeared during srach analysis lock.", chatID)
				b.settingsMutex.Unlock() // Unlock 4d (error path)
			}
		} else {
			if b.config.Debug {
				log.Printf("[DEBUG][MH] Chat %d: Srach analysis disabled.", chatID)
			}
		} // End Srach Analysis block

		// If message was not handled by srach logic, check other conditions
		if !srachHandled {
			if b.config.Debug {
				log.Printf("[DEBUG][MH] Chat %d: Message not handled by Srach logic. Proceeding to direct/AI response.", chatID)
			}
			// Проверяем, является ли сообщение прямым ответом боту или упоминанием
			isReplyToBot := message.ReplyToMessage != nil && message.ReplyToMessage.From.ID == b.api.Self.ID
			mentionsBot := false
			if message.Entities != nil {
				for _, entity := range message.Entities {
					if entity.Type == "mention" {
						mentionText := message.Text[entity.Offset : entity.Offset+entity.Length]
						if mentionText == "@"+b.api.Self.UserName {
							mentionsBot = true
							break
						}
					}
				}
			}
			if b.config.Debug {
				log.Printf("[DEBUG][MH] Chat %d: Checking for reply to bot or mention.", chatID)
				log.Printf("[DEBUG][MH] Chat %d: IsReplyToBot: %t, MentionsBot: %t.", chatID, isReplyToBot, mentionsBot)
			}

			if isReplyToBot || mentionsBot {
				// --- Проверка лимита прямых обращений ---
				if b.checkDirectReplyLimit(chatID, message.From.ID) {
					// Лимит превышен, отправляем специальный ответ
					b.sendDirectLimitExceededReply(chatID, message.MessageID)
				} else {
					// Лимит не превышен, обрабатываем как обычно
					b.sendDirectResponse(chatID, message)
				}
			} else {
				// Increment counter and check conditions for AI response
				if b.config.Debug {
					log.Printf("[DEBUG][MH] Chat %d: No direct mention. Checking conditions for AI response.", chatID)
				}
				shouldReply := false
				// Lock *only* to read/update message count
				b.settingsMutex.Lock()                                  // Lock 5 (Write lock for AI response check)
				if settings, exists := b.chatSettings[chatID]; exists { // Re-fetch settings
					if b.config.Debug {
						log.Printf("[DEBUG][MH] Chat %d: Settings mutex locked for AI response check.", chatID)
					}
					settings.MessageCount++
					if b.config.Debug {
						log.Printf("[DEBUG][MH] Chat %d: Message count incremented to %d.", chatID, settings.MessageCount)
					}
					// Generate random message count for next reply using local min/max
					targetMessages := localMinMessages + int(b.randSource.Float64()*float64(localMaxMessages-localMinMessages+1))
					shouldReply = settings.MessageCount >= targetMessages

					if b.config.Debug {
						log.Printf("[DEBUG][MH] Chat %d: Checking AI reply condition: Count(%d) >= Target(%d)? -> %t", chatID, settings.MessageCount, targetMessages, shouldReply)
					}

					if shouldReply {
						settings.MessageCount = 0 // Reset counter
						if b.config.Debug {
							log.Printf("[DEBUG][MH] Chat %d: Resetting message count.", chatID)
						}
					}
				} else {
					log.Printf("[ERROR][MH] Chat %d: Settings disappeared during AI response check lock.", chatID)
				}
				b.settingsMutex.Unlock() // Unlock 5
				// --- Settings mutex unlocked ---

				if b.config.Debug {
					log.Printf("[DEBUG][MH] Chat %d: Settings mutex unlocked after AI response check. ShouldReply: %t.", chatID, shouldReply)
				}

				if shouldReply {
					// Send AI response
					if b.config.Debug {
						log.Printf("[DEBUG][MH] Chat %d: Sending AI response.", chatID)
					}
					b.sendAIResponse(chatID)
				}
			}
		} else {
			if b.config.Debug {
				log.Printf("[DEBUG][MH] Chat %d: Message handled by Srach logic. Skipping direct/AI response.", chatID)
			}
		}
	}

	if b.config.Debug {
		log.Printf("[DEBUG][MH] Chat %d: Exiting handleMessage normally.", chatID)
		processingTime := time.Since(startTime)
		log.Printf("[DEBUG][MH] Chat %d: Message processing time: %v", chatID, processingTime)
	}
}

// sendReplyAndDeleteAfter отправляет сообщение и планирует его удаление через указанное время.
// Возвращает отправленное сообщение и ошибку (если была).
func (b *Bot) sendReplyAndDeleteAfter(chatID int64, text string, delay time.Duration) (*tgbotapi.Message, error) {
	msg := tgbotapi.NewMessage(chatID, text)
	sentMsg, err := b.api.Send(msg)
	if err != nil {
		return nil, fmt.Errorf("ошибка отправки временного сообщения: %w", err)
	}

	// Запускаем удаление в фоне
	go func() {
		time.Sleep(delay)
		b.deleteMessage(chatID, sentMsg.MessageID)
	}()

	return &sentMsg, nil
}

// sendDirectResponse отправляет ответ на прямое упоминание или ответ боту
func (b *Bot) sendDirectResponse(chatID int64, message *tgbotapi.Message) {
	log.Printf("[DEBUG][MH][DirectResponse] Chat %d: Handling direct response to message ID %d", chatID, message.MessageID)

	// 1. Получаем текст текущего сообщения
	messageText := message.Text
	if messageText == "" && message.Caption != "" {
		messageText = message.Caption
	}
	if messageText == "" {
		log.Printf("[WARN][MH][DirectResponse] Chat %d: Сообщение (ID %d) не содержит текста для ответа.", chatID, message.MessageID)
		b.sendReply(chatID, "И?")
		return
	}

	// 2. Классифицируем сообщение (serious или casual)
	classifyPrompt := b.config.ClassifyDirectMessagePrompt
	classification, errClassify := b.llm.GenerateArbitraryResponse(classifyPrompt, messageText)
	if errClassify != nil {
		log.Printf("[ERROR][MH][DirectResponse] Chat %d: Ошибка классификации сообщения: %v. Используем casual ответ.", chatID, errClassify)
		classification = "casual"
	}
	classification = strings.TrimSpace(strings.ToLower(classification))
	log.Printf("[DEBUG][MH][DirectResponse] Chat %d: Сообщение ID %d классифицировано как '%s'.", chatID, message.MessageID, classification)

	// 3. Собираем контекст для ответа
	// 3.1 Получаем историю из краткосрочной памяти
	history := b.storage.GetMessages(chatID)
	// Ограничиваем историю
	if len(history) > b.config.ContextWindow {
		history = history[len(history)-b.config.ContextWindow:]
	}

	// 3.2 Получаем релевантные сообщения из долгосрочной памяти (если включено)
	var relevantMessages []*tgbotapi.Message
	if b.config.LongTermMemoryEnabled {
		relMsgs, errSearch := b.storage.SearchRelevantMessages(chatID, messageText, b.config.LongTermMemoryFetchK)
		if errSearch != nil {
			log.Printf("[WARN][MH][DirectResponse] Chat %d: Ошибка поиска в долгосрочной памяти: %v", chatID, errSearch)
		} else {
			relevantMessages = relMsgs
			if b.config.Debug && len(relevantMessages) > 0 {
				log.Printf("[DEBUG][MH][DirectResponse] Chat %d: Найдено %d релевантных сообщений в долгосрочной памяти.", chatID, len(relevantMessages))
			}
		}
	}

	// 3.3 Объединяем и форматируем всю историю для LLM
	// Сначала добавляем релевантные из долгосрочной памяти (если есть)
	combinedHistory := make([]*tgbotapi.Message, 0, len(relevantMessages)+len(history))
	combinedHistory = append(combinedHistory, relevantMessages...)
	// Затем добавляем историю из краткосрочной (избегая дубликатов, хотя SearchRelevantMessages должен сам их отсеивать от недавних)
	seenIDs := make(map[int]bool)
	for _, msg := range relevantMessages {
		seenIDs[msg.MessageID] = true
	}
	for _, msg := range history {
		if !seenIDs[msg.MessageID] {
			combinedHistory = append(combinedHistory, msg)
			seenIDs[msg.MessageID] = true
		}
	}
	// НЕ добавляем текущее message в combinedHistory, оно будет частью contextText

	contextText := formatHistoryWithProfiles(chatID, combinedHistory, b.storage, b.config, b.llm, b.config.Debug, b.config.TimeZone)
	// Добавляем текущее сообщение в конец сформированного контекста
	formattedCurrentMsg := formatSingleMessage(message, nil, time.Local) // Форматируем без профилей, т.к. они уже в contextText
	contextText += "\n" + formattedCurrentMsg                            // Добавляем текущее сообщение

	// 4. Выбираем промпт
	var finalPrompt string
	if classification == "serious" {
		finalPrompt = b.config.SeriousDirectPrompt
	} else {
		finalPrompt = b.config.DirectPrompt
	}

	// 5. Генерируем ответ, используя GenerateResponseFromTextContext
	responseText, errGen := b.llm.GenerateResponseFromTextContext(finalPrompt, contextText)
	if errGen != nil {
		log.Printf("[ERROR][MH][DirectResponse] Chat %d: Ошибка генерации ответа (тип '%s'): %v", chatID, classification, errGen)
		if responseText == "[Лимит]" || responseText == "[Заблокировано]" {
			// Ошибка уже залогирована, просто используем текст ошибки
		} else {
			responseText = "Чет я завис."
		}
	}

	// 6. Отправляем ответ
	msg := tgbotapi.NewMessage(chatID, responseText)
	msg.ReplyToMessageID = message.MessageID

	_, errSend := b.api.Send(msg)
	if errSend != nil {
		log.Printf("[ERROR][MH][DirectResponse] Chat %d: Ошибка отправки ответа: %v", chatID, errSend)
	}
}

// sendAIResponse генерирует и отправляет ответ с помощью AI
func (b *Bot) sendAIResponse(chatID int64) {
	// --- Загрузка истории сообщений ---
	history := b.storage.GetMessages(chatID) // Получаем всю доступную историю

	// Ограничиваем историю до contextWindow, если она слишком большая
	if len(history) > b.config.ContextWindow {
		if b.config.Debug {
			log.Printf("[DEBUG][sendAIResponse] Чат %d: История (%d) больше окна (%d), обрезаю.", chatID, len(history), b.config.ContextWindow)
		}
		history = history[len(history)-b.config.ContextWindow:]
	}

	// --- Форматирование контекста с профилями ---
	// Передаем cfg и llmClient для работы долгосрочной памяти
	contextText := formatHistoryWithProfiles(chatID, history, b.storage, b.config, b.llm, b.config.Debug, b.config.TimeZone)
	if contextText == "" {
		log.Printf("[WARN][sendAIResponse] Чат %d: Не удалось сформировать контекст для AI (возможно, нет сообщений или профилей).", chatID)
		// Можно отправить сообщение об ошибке или просто ничего не делать
		// b.sendReply(chatID, "Не смог подготовить данные для ответа.")
		return
	}

	// --- Получение промпта ---
	// Используем основной промпт по умолчанию
	prompt := b.config.DefaultPrompt
	// TODO: Проверить, нужен ли CustomPrompt из настроек чата?
	// b.settingsMutex.RLock()
	// if settings, exists := b.chatSettings[chatID]; exists && settings.CustomPrompt != "" {
	// 	prompt = settings.CustomPrompt
	// }
	// b.settingsMutex.RUnlock()

	if b.config.Debug {
		log.Printf("[DEBUG][sendAIResponse] Чат %d: Вызываю LLM с отформатированным контекстом (длина: %d байт).", chatID, len(contextText))
		// Можно логировать начало контекста, если нужно
		// log.Printf("[DEBUG][sendAIResponse] Context start: %s...", truncateString(contextText, 150))
	}

	// --- Вызов LLM с отформатированным контекстом ---
	response, err := b.llm.GenerateResponseFromTextContext(prompt, contextText)
	if err != nil {
		log.Printf("[ERROR][sendAIResponse] Чат %d: Ошибка генерации ответа LLM: %v", chatID, err)
		// Обработка специфических ошибок (лимит, блокировка), если они не обработаны в клиенте LLM
		if response == "[Лимит]" || response == "[Заблокировано]" {
			log.Printf("[WARN][sendAIResponse] Чат %d: Ответ LLM был '[Лимит]' или '[Заблокировано]'. Не отправляем сообщение.", chatID)
			// Можно отправить пользователю кастомное сообщение
		} else {
			// Общая ошибка
			// b.sendReply(chatID, "Извините, произошла ошибка при генерации ответа.")
		}
		return
	}

	if response == "" || response == "[Лимит]" || response == "[Заблокировано]" {
		if b.config.Debug {
			log.Printf("[DEBUG][sendAIResponse] Чат %d: Получен пустой ответ, '[Лимит]' или '[Заблокировано]' от LLM. Ответ не отправлен.", chatID)
		}
		return
	}

	// --- Отправка ответа ---
	if b.config.Debug {
		log.Printf("[DEBUG][sendAIResponse] Чат %d: AI сгенерировал ответ: %s", chatID, response)
	}
	b.sendReply(chatID, response) // Отправляем сгенерированный ответ
}

// --- Вспомогательная функция для обрезки ---
// Можно вынести в helpers.go, если еще не там
/*
func truncateString(s string, maxLen int) string {
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	if maxLen < 3 {
		return string(runes[:maxLen])
	}
	return string(runes[:maxLen-3]) + "..."
}
*/

// downloadFile скачивает файл по URL и возвращает его содержимое
func downloadFile(url string) ([]byte, error) {
	resp, err := http.Get(url)
	if err != nil {
		return nil, fmt.Errorf("ошибка HTTP GET для %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		bodyBytes, _ := io.ReadAll(resp.Body) // Читаем тело ответа для лога
		return nil, fmt.Errorf("не удалось скачать файл, статус: %d, тело: %s", resp.StatusCode, string(bodyBytes))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("ошибка чтения тела ответа: %w", err)
	}
	return body, nil
}

// handlePendingSettingInput обрабатывает ввод пользователя для ожидаемой настройки
func (b *Bot) handlePendingSettingInput(chatID int64, userID int64, username string, pendingSettingKey string, inputText string) error {
	var err error
	var successMessage string
	var nextPendingSetting string // Для цепочки ввода (например, лимиты)
	var nextPrompt string         // Промпт для следующего шага

	inputText = strings.TrimSpace(inputText)
	valueInt := 0 // Для числовых значений

	if pendingSettingKey == "direct_limit_count" || pendingSettingKey == "direct_limit_duration" {
		valueInt, err = strconv.Atoi(inputText)
		if err != nil {
			b.sendReply(chatID, fmt.Sprintf("🚫 '%s' - это не число. Попробуйте еще раз.", inputText))
			// Не сбрасываем PendingSetting, чтобы пользователь мог повторить ввод
			return fmt.Errorf("некорректный ввод числа")
		}
	}

	// Обработка конкретных ожидаемых настроек
	switch pendingSettingKey {
	case "profile_data":
		// Логика парсинга и сохранения профиля
		targetUsername, _, alias, gender, realName, bio, parseErr := parseProfileArgs(inputText)
		if parseErr != nil {
			b.sendReply(chatID, fmt.Sprintf("🚫 Ошибка парсинга данных профиля: %v", parseErr))
			return parseErr
		}

		// Находим ID пользователя по имени
		targetUserID, findErr := b.getUserIDByUsername(chatID, targetUsername)
		if findErr != nil {
			b.sendReply(chatID, fmt.Sprintf("🚫 Не удалось найти пользователя %s в недавней истории чата для сохранения профиля.", targetUsername))
			return findErr
		}

		// Создаем или обновляем профиль
		profile := &storage.UserProfile{
			ChatID:    chatID,
			UserID:    targetUserID,
			Username:  strings.TrimPrefix(targetUsername, "@"),
			Alias:     alias,
			Gender:    gender,
			RealName:  realName,
			Bio:       bio,
			CreatedAt: time.Now(), // Установим время создания при первом сохранении
			UpdatedAt: time.Now(),
		}

		err = b.storage.SetUserProfile(profile)
		if err != nil {
			log.Printf("[ERROR][MH Pending] Admin %d: Failed to save profile for %s (%d): %v", userID, targetUsername, targetUserID, err)
			b.sendReply(chatID, fmt.Sprintf("🚫 Произошла ошибка при сохранении профиля для %s.", targetUsername))
			return err
		}
		successMessage = fmt.Sprintf("✅ Профиль для %s успешно сохранен.", targetUsername)

	case "direct_limit_count":
		if valueInt >= 0 { // 0 means unlimited
			err = b.storage.UpdateDirectLimitCount(chatID, valueInt)
			if err != nil {
				log.Printf("[ERROR][MH Pending] Chat %d: Failed to save direct_limit_count %d: %v", chatID, valueInt, err)
				b.sendReply(chatID, "🚫 Произошла ошибка при сохранении настройки лимита сообщений.")
			} else {
				log.Printf("User %d: Лимит прямых сообщений установлен: %d", chatID, valueInt)
				b.sendReply(chatID, fmt.Sprintf("✅ Лимит прямых сообщений установлен: %d", valueInt))
				// Переходим к следующему шагу - ввод длительности
				nextPendingSetting = "direct_limit_duration"
				nextPrompt = b.config.PromptEnterDirectLimitDuration
			}
		} else {
			b.sendReply(chatID, "🚫 Количество должно быть 0 или больше.")
			return fmt.Errorf("некорректное значение лимита")
		}

	case "direct_limit_duration":
		if valueInt > 0 { // Duration must be positive
			duration := time.Duration(valueInt) * time.Minute
			err = b.storage.UpdateDirectLimitDuration(chatID, duration)
			if err != nil {
				log.Printf("[ERROR][MH Pending] Chat %d: Failed to save direct_limit_duration %d mins: %v", chatID, valueInt, err)
				b.sendReply(chatID, "🚫 Произошла ошибка при сохранении настройки периода лимита.")
			} else {
				log.Printf("User %d: Период лимита установлен: %d минут.", valueInt)
				b.sendReply(chatID, fmt.Sprintf("✅ Период лимита установлен: %d минут.", valueInt))
				// Цепочка ввода завершена, обновляем клавиатуру
				// Вызываем обновление клавиатуры после успешного ввода
				go func() {
					// Небольшая задержка, чтобы сообщение об успехе успело отправиться
					time.Sleep(1 * time.Second)
					b.updateSettingsKeyboardAfterInput(chatID)
				}()
			}
		} else {
			b.sendReply(chatID, "🚫 Длительность должна быть больше 0.")
			return fmt.Errorf("некорректное значение длительности")
		}

	default:
		log.Printf("[WARN][MH Pending] Chat %d: Неизвестный pendingSettingKey: %s", chatID, pendingSettingKey)
		b.sendReply(chatID, "🤔 Не понимаю, что вы пытаетесь настроить.")
		// Сбрасываем неизвестный ключ
		b.settingsMutex.Lock() // Lock нужен для записи
		if currentSettings, exists := b.chatSettings[chatID]; exists {
			currentSettings.PendingSetting = ""
		}
		b.settingsMutex.Unlock()
		return fmt.Errorf("неизвестный pendingSettingKey")
	}

	// Если дошли сюда, значит ввод обработан успешно
	// Отправляем сообщение об успехе (если есть)
	if successMessage != "" {
		// Отправляем и удаляем через 5 секунд
		go func() {
			sentSuccessMsg, sendErr := b.sendReplyReturnMsg(chatID, successMessage)
			if sendErr == nil && sentSuccessMsg != nil {
				time.Sleep(5 * time.Second)
				b.deleteMessage(chatID, sentSuccessMsg.MessageID)
			}
		}()
	}

	// Обновляем PendingSetting и LastInfoMessageID (если есть следующий шаг)
	b.settingsMutex.Lock()
	if currentSettings, exists := b.chatSettings[chatID]; exists {
		currentSettings.PendingSetting = nextPendingSetting // Устанавливаем следующий или сбрасываем
		currentSettings.LastInfoMessageID = 0               // Сбрасываем ID инфо-сообщения
		// Если есть следующий шаг, отправляем новый запрос ввода
		if nextPendingSetting != "" && nextPrompt != "" {
			sentPromptMsg, promptErr := b.sendReplyReturnMsg(chatID, nextPrompt)
			if promptErr == nil && sentPromptMsg != nil {
				currentSettings.LastInfoMessageID = sentPromptMsg.MessageID // Сохраняем ID нового запроса
			} else {
				log.Printf("[ERROR][MH Pending] Chat %d: Failed to send next prompt '%s': %v", chatID, nextPrompt, promptErr)
				// Можно попробовать откатить pending setting?
				currentSettings.PendingSetting = pendingSettingKey     // Возвращаем предыдущий
				err = fmt.Errorf("ошибка отправки следующего запроса") // Возвращаем ошибку
			}
		}
	}
	b.settingsMutex.Unlock()

	return err // Возвращаем nil или ошибку отправки следующего запроса
}

// updateSettingsKeyboardAfterInput обновляет клавиатуру настроек после завершения ввода
// Вызывается асинхронно
func (b *Bot) updateSettingsKeyboardAfterInput(chatID int64) {
	b.settingsMutex.RLock()
	settings, exists := b.chatSettings[chatID]
	if !exists {
		b.settingsMutex.RUnlock()
		return
	}
	lastSettingsMsgID := settings.LastSettingsMessageID
	b.settingsMutex.RUnlock()

	if lastSettingsMsgID != 0 {
		// Используем фиктивный CallbackQuery для вызова updateSettingsKeyboard
		dummyMessage := tgbotapi.Message{Chat: &tgbotapi.Chat{ID: chatID}, MessageID: lastSettingsMsgID}
		dummyQuery := tgbotapi.CallbackQuery{Message: &dummyMessage, From: &tgbotapi.User{}}
		b.updateSettingsKeyboard(&dummyQuery) // Передаем фиктивный query
	}
}

// --- Функции для лимита прямых обращений ---

// checkDirectReplyLimit проверяет, превышен ли лимит для пользователя.
// Возвращает true, если лимит превышен, иначе false.
// Также добавляет текущую временную метку, если лимит не превышен.
func (b *Bot) checkDirectReplyLimit(chatID int64, userID int64) bool {
	b.settingsMutex.Lock() // Блокируем для чтения настроек и записи временных меток
	defer b.settingsMutex.Unlock()

	// Получаем настройки лимита из DB через storage
	dbSettings, err := b.storage.GetChatSettings(chatID)
	if err != nil {
		log.Printf("[ERROR][DirectLimit] Чат %d: Ошибка получения настроек из DB: %v. Лимит не проверяется.", chatID, err)
		return false // Если ошибка - лимит не применяем
	}

	// Проверяем, включен ли лимит
	limitEnabled := b.config.DirectReplyLimitEnabledDefault
	if dbSettings.DirectReplyLimitEnabled != nil {
		limitEnabled = *dbSettings.DirectReplyLimitEnabled
	}
	if !limitEnabled {
		if b.config.Debug {
			log.Printf("[DEBUG][DirectLimit] Чат %d: Лимит прямых обращений выключен.", chatID)
		}
		return false
	}

	// Получаем значения лимита
	limitCount := b.config.DirectReplyLimitCountDefault
	if dbSettings.DirectReplyLimitCount != nil {
		limitCount = *dbSettings.DirectReplyLimitCount
	}
	limitDuration := b.config.DirectReplyLimitDurationDefault
	if dbSettings.DirectReplyLimitDuration != nil {
		durationMinutes := *dbSettings.DirectReplyLimitDuration
		limitDuration = time.Duration(durationMinutes) * time.Minute
	}

	// Проверяем метки
	now := time.Now()
	// Инициализируем map для чата, если его нет
	if _, ok := b.directReplyTimestamps[chatID]; !ok {
		b.directReplyTimestamps[chatID] = make(map[int64][]time.Time)
	}
	userTimestamps := b.directReplyTimestamps[chatID][userID]
	validTimestamps := []time.Time{}

	// СНАЧАЛА добавляем новую метку
	userTimestamps = append(userTimestamps, now)

	// ТЕПЕРЬ удаляем старые метки
	for _, ts := range userTimestamps {
		if now.Sub(ts) < limitDuration {
			validTimestamps = append(validTimestamps, ts)
		}
	}

	// Обновляем метки для пользователя ПЕРЕД проверкой
	b.directReplyTimestamps[chatID][userID] = validTimestamps

	exceeded := len(validTimestamps) > limitCount // СТРОГО больше, т.к. мы уже добавили текущее

	if b.config.Debug {
		log.Printf("[DEBUG][DirectLimit] Чат %d, User %d: Проверка лимита (%d за %v). Метки после добавления/очистки: %d. Превышен: %t",
			chatID, userID, limitCount, limitDuration, len(validTimestamps), exceeded)
	}

	return exceeded
}

// sendDirectLimitExceededReply отправляет сообщение о превышении лимита.
func (b *Bot) sendDirectLimitExceededReply(chatID int64, replyToMessageID int) {
	prompt := b.config.DirectReplyLimitPrompt
	responseText, err := b.llm.GenerateArbitraryResponse(prompt, "")
	if err != nil {
		log.Printf("[ERROR][DirectLimit] Чат %d: Ошибка генерации ответа о превышении лимита: %v", chatID, err)
		responseText = "Слишком часто пишешь, отдохни."
	}

	msg := tgbotapi.NewMessage(chatID, responseText)
	msg.ReplyToMessageID = replyToMessageID
	// Отправляем и сохраняем ответ бота
	sentReply, errSend := b.api.Send(msg)
	if errSend != nil {
		log.Printf("[ERROR][DirectLimit] Чат %d: Ошибка отправки ответа о превышении лимита: %v", chatID, errSend)
	} else {
		go b.storage.AddMessage(chatID, &sentReply)
	}
}

// escapeMarkdownV2 экранирует специальные символы для MarkdownV2
// Копипаста из helpers.go
var markdownV2Escaper = strings.NewReplacer(
	"\\", "\\\\",
	"_", "\\_",
	"*", "\\*",
	"[", "\\[",
	"]", "\\]",
	"(", "\\(",
	")", "\\)",
	"~", "\\~",
	"`", "\\`",
	">", "\\>",
	"#", "\\#",
	"+", "\\+",
	"-", "\\-",
	"=", "\\=",
	"|", "\\|",
	"{", "\\{",
	"}", "\\}",
	".", "\\.",
	"!", "\\!",
)

func escapeMarkdownV2(text string) string {
	return markdownV2Escaper.Replace(text)
}
