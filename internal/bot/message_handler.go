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
		// Отправляем распознанный текст как ответ на оригинальное голосовое сообщение
		if formattedText != "" { // Убедимся, что текст не пустой
			// Форматируем текст ответа
			finalReplyText := fmt.Sprintf("🎤 Перевожу голосовуху: [_%s_]", formattedText)
			replyMsg := tgbotapi.NewMessage(chatID, finalReplyText)
			replyMsg.ReplyToMessageID = message.MessageID // Устанавливаем ReplyTo
			replyMsg.ParseMode = "Markdown"               // Устанавливаем режим Markdown для курсива
			_, replyErr := b.api.Send(replyMsg)
			if replyErr != nil {
				log.Printf("[ERROR][VoiceHandler] Чат %d: Ошибка отправки транскрибированного текста: %v", chatID, replyErr)
			}
		} // --- КОНЕЦ ОТПРАВКИ ---

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

			// Обработка конкретных ожидаемых настроек
			var confirmationMessage string
			var settingUpdated bool = false

			log.Printf("[DEBUG][MH Pending Check] Chat %d: Before switch. Value: '%s'", chatID, localPendingSetting) // ADDED LOG
			switch localPendingSetting {
			case "min_messages":
				if val, err := strconv.Atoi(message.Text); err == nil && val > 0 {
					b.settingsMutex.Lock()
					if settings, exists := b.chatSettings[chatID]; exists {
						// Проверяем, что min не больше текущего max
						if val <= settings.MaxMessages {
							settings.MinMessages = val
							settings.PendingSetting = "max_messages" // Сразу запрашиваем max
							confirmationMessage = fmt.Sprintf("✅ Минимальное количество сообщений установлено: %d.", val)
							settingUpdated = true // Помечаем, что часть настройки (min) обновлена
							// Запрашиваем ввод MaxMessages
							promptText := b.config.PromptEnterMaxMessages
							b.settingsMutex.Unlock() // Разблокируем перед отправкой

							// Удаляем старое сообщение с запросом (LastInfoMessageID) и сообщение пользователя
							b.deleteMessage(chatID, settings.LastInfoMessageID) // ID взят до Unlock
							b.deleteMessage(chatID, message.MessageID)

							// Отправляем новый запрос
							promptMsg := tgbotapi.NewMessage(chatID, promptText+"\n\nИли отправьте /cancel для отмены.")
							sentMsg, err := b.api.Send(promptMsg)
							if err != nil {
								log.Printf("[ERROR][MH] Ошибка отправки промпта для max_messages в чат %d: %v", chatID, err)
								// Сбрасываем PendingSetting обратно на min_messages, т.к. не смогли запросить max
								b.settingsMutex.Lock()
								if set, ok := b.chatSettings[chatID]; ok {
									set.PendingSetting = "min_messages"
								}
								b.settingsMutex.Unlock()
							} else {
								// Сохраняем новый ID промпта
								b.settingsMutex.Lock()
								if set, ok := b.chatSettings[chatID]; ok {
									set.LastInfoMessageID = sentMsg.MessageID
								}
								b.settingsMutex.Unlock()
							}
							return // Выходим, ждем ввода max_messages

						} else {
							confirmationMessage = fmt.Sprintf("❌ Ошибка: Минимальное значение (%d) не может быть больше максимального (%d).", val, settings.MaxMessages)
						}
					}
					b.settingsMutex.Unlock()
				} else {
					confirmationMessage = "❌ Неверный формат. Введите положительное число."
				}

			case "max_messages":
				if val, err := strconv.Atoi(message.Text); err == nil && val > 0 {
					b.settingsMutex.Lock()
					if settings, exists := b.chatSettings[chatID]; exists {
						// Проверяем, что max не меньше текущего min
						if val >= settings.MinMessages {
							settings.MaxMessages = val
							settings.PendingSetting = "" // Завершили ввод интервала
							confirmationMessage = fmt.Sprintf("✅ Максимальное количество сообщений установлено: %d.", val)
							settingUpdated = true
						} else {
							confirmationMessage = fmt.Sprintf("❌ Ошибка: Максимальное значение (%d) не может быть меньше минимального (%d).", val, settings.MinMessages)
						}
					}
					b.settingsMutex.Unlock()
				} else {
					confirmationMessage = "❌ Неверный формат. Введите положительное число."
				}

			case "daily_time":
				if val, err := strconv.Atoi(message.Text); err == nil && val >= 0 && val <= 23 {
					b.settingsMutex.Lock()
					if settings, exists := b.chatSettings[chatID]; exists {
						settings.DailyTakeTime = val
						settings.PendingSetting = ""
						confirmationMessage = fmt.Sprintf("✅ Время ежедневной темы установлено: %02d:00.", val)
						settingUpdated = true
					}
					b.settingsMutex.Unlock()
				} else {
					confirmationMessage = "❌ Неверный формат. Введите час от 0 до 23."
				}

			case "summary_interval":
				if val, err := strconv.Atoi(message.Text); err == nil && val >= 0 {
					b.settingsMutex.Lock()
					if settings, exists := b.chatSettings[chatID]; exists {
						settings.SummaryIntervalHours = val
						settings.PendingSetting = ""
						if val == 0 {
							confirmationMessage = "✅ Автоматическое саммари отключено."
						} else {
							confirmationMessage = fmt.Sprintf("✅ Интервал автоматического саммари установлен: %d ч.", val)
						}
						settingUpdated = true
					}
					b.settingsMutex.Unlock()
				} else {
					confirmationMessage = "❌ Неверный формат. Введите не отрицательное число (0 для отключения)."
				}

			default:
				// Эта ветка не должна вызываться, если localPendingSetting не пустой,
				// но на всякий случай оставим лог.
				log.Printf("[WARN][MH] Chat %d: Получено сообщение '%s' при ожидании НЕИЗВЕСТНОЙ/НЕОБРАБОТАННОЙ настройки '%s'.", chatID, message.Text, localPendingSetting) // Modified Log slightly
				confirmationMessage = fmt.Sprintf("Получено '%s', но я ожидал значение для '%s'. Настройка не изменена. Используйте /settings для повтора.", message.Text, localPendingSetting)
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
				if settingUpdated || strings.HasPrefix(confirmationMessage, "❌") {
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
			b.sendReplyAndDeleteAfter(chatID, confirmationMessage, 10*time.Second) // Удаляем через 10 секунд

			// Если настройка была успешно обновлена И ввод завершен (не ждем max_messages),
			// возвращаемся к клавиатуре настроек.
			b.settingsMutex.RLock()
			pendingSettingAfterUpdate := ""
			if settings, exists := b.chatSettings[chatID]; exists {
				pendingSettingAfterUpdate = settings.PendingSetting
			}
			b.settingsMutex.RUnlock()

			if settingUpdated && pendingSettingAfterUpdate == "" {
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

		// Обновляем профиль пользователя (используем From из textMessage/originalMessage)
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
			// Check if the message is a reply to the bot or mentions the bot
			isReplyToBot := message.ReplyToMessage != nil && message.ReplyToMessage.From.UserName == b.api.Self.UserName
			mentionsBot := strings.Contains(message.Text, "@"+b.api.Self.UserName)

			if b.config.Debug {
				log.Printf("[DEBUG][MH] Chat %d: Checking for reply to bot or mention.", chatID)
				log.Printf("[DEBUG][MH] Chat %d: IsReplyToBot: %t, MentionsBot: %t.", chatID, isReplyToBot, mentionsBot)
			}

			if isReplyToBot || mentionsBot {
				// Send direct response
				if b.config.Debug {
					log.Printf("[DEBUG][MH] Chat %d: Sending direct response.", chatID)
				}
				b.sendDirectResponse(chatID, message)
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

	// Используем DIRECT_PROMPT
	directPrompt := b.config.DirectPrompt

	// Генерируем ответ, передавая пустую историю и текущее сообщение
	responseText, err := b.llm.GenerateResponse(directPrompt, nil, message) // Передаем nil для history
	if err != nil {
		log.Printf("Ошибка генерации прямого ответа для чата %d: %v", chatID, err)
		return
	}

	// Отправляем ответ (возможно, как реплай на исходное сообщение)
	msg := tgbotapi.NewMessage(chatID, responseText)
	msg.ReplyToMessageID = message.MessageID // Отвечаем на сообщение пользователя
	msg.ParseMode = "Markdown"

	_, err = b.api.Send(msg)
	if err != nil {
		log.Printf("Ошибка отправки прямого ответа в чат %d: %v", chatID, err)
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
	contextText := formatHistoryWithProfiles(chatID, history, b.storage, b.config.Debug)
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
