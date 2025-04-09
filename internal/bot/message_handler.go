package bot

import (
	"fmt"
	"log"
	"math/rand"
	"strconv"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// handleMessage processes regular user messages (not commands or callbacks)
func (b *Bot) handleMessage(update tgbotapi.Update) {
	message := update.Message // Работаем с message внутри этой функции
	chatID := message.Chat.ID
	// userID := message.From.ID // Пока не используется здесь
	text := message.Text
	messageTime := message.Time()

	log.Printf("[DEBUG][MH] Chat %d: Entering handleMessage for message ID %d.", chatID, message.MessageID)

	// --- Инициализация настроек чата (если нет) ---
	// Этот блок выполняется в handleUpdate перед вызовом handleMessage
	b.settingsMutex.RLock()
	settings, exists := b.chatSettings[chatID]
	if !exists {
		// Если настроек нет даже после handleUpdate, что-то пошло не так
		b.settingsMutex.RUnlock()
		log.Printf("[ERROR][MH] Chat %d: Настройки не найдены в handleMessage!", chatID)
		return
	}
	b.settingsMutex.RUnlock()
	log.Printf("[DEBUG][MH] Chat %d: Settings found.", chatID)
	// --- Конец инициализации (проверки) ---

	// --- Обработка отмены ввода ---
	b.settingsMutex.RLock()
	currentPendingSetting := settings.PendingSetting
	b.settingsMutex.RUnlock()
	if text == "/cancel" && currentPendingSetting != "" {
		log.Printf("[DEBUG][MH] Chat %d: Handling /cancel for pending setting '%s'.", chatID, currentPendingSetting)
		// Получаем ID сообщения с промптом ДО сброса PendingSetting
		var lastInfoMsgID int
		b.settingsMutex.Lock()
		if settings, exists := b.chatSettings[chatID]; exists {
			lastInfoMsgID = settings.LastInfoMessageID
			settings.PendingSetting = ""   // Сбрасываем ожидание
			settings.LastInfoMessageID = 0 // Сбрасываем ID промпта
		}
		b.settingsMutex.Unlock()
		log.Printf("[DEBUG][MH] Chat %d: Pending setting reset.", chatID)

		// Удаляем сообщение с промптом (если был ID)
		if lastInfoMsgID != 0 {
			b.deleteMessage(chatID, lastInfoMsgID)
		}
		// Удаляем сообщение пользователя с /cancel
		b.deleteMessage(chatID, message.MessageID)

		b.sendReply(chatID, "Ввод отменен.")
		// ID сообщения с настройками будет 0, т.к. мы удалили исходное сообщение и не можем передать его ID
		b.sendSettingsKeyboard(chatID, 0) // Показываем меню настроек снова
		log.Printf("[DEBUG][MH] Chat %d: Exiting handleMessage after /cancel.", chatID)
		return
	}
	// --- Конец обработки отмены ---

	// --- Обработка ввода ожидаемой настройки ---
	if currentPendingSetting != "" {
		log.Printf("[DEBUG][MH] Chat %d: Handling input for pending setting '%s'. Input: '%s'", chatID, currentPendingSetting, text)
		isValidInput := false
		parsedValue, err := strconv.Atoi(text)
		var lastInfoMsgID int // Для удаления сообщения с промптом

		if err != nil {
			log.Printf("[DEBUG][MH] Chat %d: Input parsing error: %v", chatID, err)
			b.sendReply(chatID, "Ошибка: Введите числовое значение или /cancel для отмены.")
		} else {
			log.Printf("[DEBUG][MH] Chat %d: Input parsed successfully: %d", chatID, parsedValue)
			b.settingsMutex.Lock() // Блокируем для изменения настроек
			log.Printf("[DEBUG][MH] Chat %d: Settings mutex locked for update.", chatID)
			switch currentPendingSetting {
			case "min_messages":
				log.Printf("[DEBUG][MH] Chat %d: Processing 'min_messages'.", chatID)
				if parsedValue > 0 {
					settings.MinMessages = parsedValue
					isValidInput = true
					// Теперь запрашиваем максимальное значение
					settings.PendingSetting = "max_messages"
					promptText := b.config.PromptEnterMaxMessages
					log.Printf("[DEBUG][MH] Chat %d: MinMessages set to %d. Requesting MaxMessages.", chatID, parsedValue)
					b.settingsMutex.Unlock() // Разблокируем перед отправкой ответа
					log.Printf("[DEBUG][MH] Chat %d: Settings mutex unlocked before sending MaxMessages prompt.", chatID)
					// Удаляем сообщение с промптом MinMessages И сообщение пользователя с вводом
					if lastInfoMsgID != 0 {
						b.deleteMessage(chatID, lastInfoMsgID)
					}
					b.deleteMessage(chatID, message.MessageID)
					// Отправляем новый промпт и сохраняем его ID
					promptMsg := tgbotapi.NewMessage(chatID, promptText+"\n\nИли отправьте /cancel для отмены.")
					sentMsg, sendErr := b.api.Send(promptMsg)
					if sendErr != nil {
						log.Printf("Ошибка отправки промпта MaxMessages: %v", sendErr)
						// Пытаемся откатить PendingSetting
						b.settingsMutex.Lock()
						if settings, exists := b.chatSettings[chatID]; exists {
							settings.PendingSetting = "" // Отменяем ожидание
						}
						b.settingsMutex.Unlock()
					} else {
						// Сохраняем ID нового промпта
						b.settingsMutex.Lock()
						if settings, exists := b.chatSettings[chatID]; exists {
							settings.LastInfoMessageID = sentMsg.MessageID
						}
						b.settingsMutex.Unlock()
					}
					// Важно: Не показываем меню настроек здесь, так как ждем следующего ввода
					log.Printf("[DEBUG][MH] Chat %d: Exiting handleMessage, waiting for MaxMessages input.", chatID)
					return // Выходим, чтобы дождаться ввода максимального значения
				} else {
					b.sendReply(chatID, "Ошибка: Минимальное значение должно быть больше 0.")
				}
			case "max_messages":
				log.Printf("[DEBUG][MH] Chat %d: Processing 'max_messages'.", chatID)
				if parsedValue >= settings.MinMessages {
					settings.MaxMessages = parsedValue
					isValidInput = true
					log.Printf("[DEBUG][MH] Chat %d: MaxMessages set to %d.", chatID, parsedValue)
				} else {
					b.sendReply(chatID, fmt.Sprintf("Ошибка: Максимальное значение должно быть не меньше минимального (%d).", settings.MinMessages))
				}
			case "daily_time":
				log.Printf("[DEBUG][MH] Chat %d: Processing 'daily_time'.", chatID)
				if parsedValue >= 0 && parsedValue <= 23 {
					settings.DailyTakeTime = parsedValue
					log.Printf("[DEBUG][MH] Chat %d: DailyTakeTime set to %d. Настройка времени тейка изменена на %d для чата %d. Перезапустите бота для применения ко всем чатам или реализуйте динамическое обновление.", chatID, parsedValue, parsedValue, chatID)
					isValidInput = true
				} else {
					b.sendReply(chatID, "Ошибка: Введите час от 0 до 23.")
				}
			case "summary_interval":
				log.Printf("[DEBUG][MH] Chat %d: Processing 'summary_interval'.", chatID)
				if parsedValue >= 0 { // 0 - выключено
					settings.SummaryIntervalHours = parsedValue
					settings.LastAutoSummaryTime = time.Time{} // Сбрасываем таймер при изменении интервала
					log.Printf("[DEBUG][MH] Chat %d: SummaryIntervalHours set to %d. Интервал авто-саммари для чата %d изменен на %d ч.", chatID, parsedValue, chatID, parsedValue)
					isValidInput = true
				} else {
					b.sendReply(chatID, "Ошибка: Интервал не может быть отрицательным (0 - выключить).")
				}
			}

			if isValidInput {
				// Сбрасываем ожидание только если ввод был валидным И это не был ввод min_messages
				if currentPendingSetting != "min_messages" {
					settings.PendingSetting = ""
					log.Printf("[DEBUG][MH] Chat %d: Pending setting reset after successful input.", chatID)
					b.sendReply(chatID, "Настройка успешно обновлена!")
				}
			}
			b.settingsMutex.Unlock() // Разблокируем после изменения
			log.Printf("[DEBUG][MH] Chat %d: Settings mutex unlocked after update.", chatID)

			// Удаляем сообщение с промптом и сообщение пользователя с вводом
			if lastInfoMsgID != 0 {
				b.deleteMessage(chatID, lastInfoMsgID)
			}
			b.deleteMessage(chatID, message.MessageID)

			// Показываем обновленное меню только если настройка завершена (не после min_messages)
			if isValidInput && currentPendingSetting != "min_messages" {
				log.Printf("[DEBUG][MH] Chat %d: Showing updated settings keyboard.", chatID)
				// ID предыдущего сообщения (промпта или ввода) уже удалено, передаем 0
				b.sendSettingsKeyboard(chatID, 0) // Показываем обновленное меню
			}
		}
		log.Printf("[DEBUG][MH] Chat %d: Exiting handleMessage after processing pending setting.", chatID)
		return // Прекращаем дальнейшую обработку сообщения, т.к. это был ввод настройки
	}
	// --- Конец обработки ввода ---

	log.Printf("[DEBUG][MH] Chat %d: No pending setting. Proceeding to normal message handling.", chatID)

	// >>> ДОБАВЛЯЕМ СООБЩЕНИЕ В ХРАНИЛИЩЕ <<<
	// Делаем это здесь, после всех проверок на команды, отмены, ввод настроек,
	// чтобы команды и системные сообщения не попадали в историю для AI.
	if message != nil && message.Text != "" && !message.IsCommand() { // Добавляем проверку, что это не команда и текст не пустой
		log.Printf("[DEBUG][MH] Chat %d: Adding message ID %d to storage.", chatID, message.MessageID)
		b.storage.AddMessage(chatID, message) // Вызов метода интерфейса
	} else {
		log.Printf("[DEBUG][MH] Chat %d: Message ID %d skipped for storage (nil, empty text, or command).", chatID, message.MessageID)
	}
	// >>> КОНЕЦ ДОБАВЛЕНИЯ В ХРАНИЛИЩЕ <<<

	log.Printf("[DEBUG][MH] Chat %d: Proceeding to Srach Analysis.", chatID)

	// Команды обрабатываются в handleUpdate перед вызовом handleMessage

	// --- Логика Анализа Срачей ---
	b.settingsMutex.RLock()
	srachEnabled := settings.SrachAnalysisEnabled
	b.settingsMutex.RUnlock()
	log.Printf("[DEBUG][MH] Chat %d: Srach analysis enabled: %t.", chatID, srachEnabled)

	srachHandled := false // Флаг, что сообщение обработано логикой срача
	if srachEnabled {
		log.Printf("[DEBUG][MH] Chat %d: Checking for potential srach trigger.", chatID)
		isPotentialSrachMsg := b.isPotentialSrachTrigger(message) // Передаем *tgbotapi.Message
		log.Printf("[DEBUG][MH] Chat %d: Is potential srach trigger: %t.", chatID, isPotentialSrachMsg)

		b.settingsMutex.Lock()
		log.Printf("[DEBUG][MH] Chat %d: Settings mutex locked for Srach logic.", chatID)
		currentState := settings.SrachState
		lastTriggerTime := settings.LastSrachTriggerTime
		log.Printf("[DEBUG][MH] Chat %d: Current Srach state: %s.", chatID, currentState)

		if isPotentialSrachMsg {
			if settings.SrachState == "none" {
				log.Printf("[DEBUG][MH] Chat %d: Srach state 'none', detecting new srach.", chatID)
				settings.SrachState = "detected"
				settings.SrachStartTime = messageTime
				settings.SrachMessages = []string{formatMessageForAnalysis(message)} // Передаем *tgbotapi.Message
				settings.LastSrachTriggerTime = messageTime
				settings.SrachLlmCheckCounter = 0
				b.settingsMutex.Unlock() // Разблокируем перед отправкой
				log.Printf("[DEBUG][MH] Chat %d: Settings mutex unlocked before sending srach warning.", chatID)
				b.sendSrachWarning(chatID)
				log.Printf("[DEBUG][MH] Chat %d: Potential srach detected.", chatID)
				srachHandled = true // Считаем сообщение обработанным (начало срача)
			} else if settings.SrachState == "detected" {
				log.Printf("[DEBUG][MH] Chat %d: Srach state 'detected', adding message to srach.", chatID)
				settings.SrachMessages = append(settings.SrachMessages, formatMessageForAnalysis(message)) // Передаем *tgbotapi.Message
				settings.LastSrachTriggerTime = messageTime
				settings.SrachLlmCheckCounter++
				log.Printf("[DEBUG][MH] Chat %d: Srach message added. Counter: %d", chatID, settings.SrachLlmCheckCounter)

				const llmCheckInterval = 3
				if settings.SrachLlmCheckCounter%llmCheckInterval == 0 {
					msgTextToCheck := message.Text
					log.Printf("[DEBUG][MH] Chat %d: Triggering LLM srach confirmation for message ID %d.", chatID, message.MessageID)
					// --- Важно: разблокировать мьютекс ПЕРЕД запуском горутины LLM ---
					b.settingsMutex.Unlock()
					log.Printf("[DEBUG][MH] Chat %d: Settings mutex unlocked before LLM confirmation goroutine.", chatID)
					go func() {
						log.Printf("[DEBUG][MH][Goroutine] Chat %d: Starting LLM srach confirmation.", chatID)
						isConfirmed := b.confirmSrachWithLLM(chatID, msgTextToCheck)
						log.Printf("[DEBUG][MH][Goroutine] Chat %d: LLM srach confirmation finished. Result: %t", chatID, isConfirmed)
						// Не делаем действий с settings здесь, т.к. горутина
					}()
					// Мьютекс уже разблокирован, не нужно разблокировать снова
				} else {
					b.settingsMutex.Unlock() // Разблокируем, если LLM не вызывался
					log.Printf("[DEBUG][MH] Chat %d: Settings mutex unlocked after adding srach message (no LLM check).", chatID)
				}
				srachHandled = true // Считаем сообщение обработанным (продолжение срача)
			} else {
				// Если state = analyzing, ничего не делаем, просто разблокируем
				b.settingsMutex.Unlock()
				log.Printf("[DEBUG][MH] Chat %d: Settings mutex unlocked (srach state '%s', not 'none' or 'detected').", chatID, settings.SrachState)
			}
		} else if currentState == "detected" {
			// Сообщение не триггер, но срач был активен
			log.Printf("[DEBUG][MH] Chat %d: Srach state 'detected', but message is not a trigger. Adding message and checking timeout.", chatID)
			settings.SrachMessages = append(settings.SrachMessages, formatMessageForAnalysis(message)) // Передаем *tgbotapi.Message

			// Проверка на завершение срача по таймеру
			const srachTimeout = 5 * time.Minute
			timeSinceLastTrigger := messageTime.Sub(lastTriggerTime)
			log.Printf("[DEBUG][MH] Chat %d: Time since last trigger: %v. Timeout: %v.", chatID, timeSinceLastTrigger, srachTimeout)
			if !lastTriggerTime.IsZero() && timeSinceLastTrigger > srachTimeout {
				log.Printf("[DEBUG][MH] Chat %d: Srach timeout reached. Starting analysis.", chatID)
				// --- Важно: разблокировать мьютекс ПЕРЕД запуском горутины анализа ---
				b.settingsMutex.Unlock()
				log.Printf("[DEBUG][MH] Chat %d: Settings mutex unlocked before analysis goroutine.", chatID)
				go b.analyseSrach(chatID)
				srachHandled = true // Сообщение обработано (последнее перед анализом)
			} else {
				b.settingsMutex.Unlock() // Просто добавили сообщение, разблокируем
				log.Printf("[DEBUG][MH] Chat %d: Settings mutex unlocked after adding non-trigger message (no timeout).", chatID)
				srachHandled = true // Считаем обработанным (часть активного срача)
			}
		} else {
			// Срач не активен и сообщение не триггер
			b.settingsMutex.Unlock()
			log.Printf("[DEBUG][MH] Chat %d: Settings mutex unlocked (no active srach, not a trigger).", chatID)
		}
	} else {
		log.Printf("[DEBUG][MH] Chat %d: Srach analysis is disabled.", chatID)
	}
	// --- Конец Логики Анализа Срачей ---

	// Если сообщение было частью срача (начало, продолжение, конец),
	// то дальнейшую обработку (прямой ответ, AI ответ) пропускаем.
	if srachHandled {
		log.Printf("[DEBUG][MH] Chat %d: Message handled by Srach logic. Exiting handleMessage.", chatID)
		return
	}

	log.Printf("[DEBUG][MH] Chat %d: Message not handled by Srach logic. Proceeding to direct/AI response.", chatID)

	// --- Обработка обычных сообщений (не срач, не ввод настроек, не команда) ---

	// Проверяем, является ли сообщение ответом на сообщение бота или обращением к боту
	log.Printf("[DEBUG][MH] Chat %d: Checking for reply to bot or mention.", chatID)
	isReplyToBot := message.ReplyToMessage != nil &&
		message.ReplyToMessage.From != nil &&
		message.ReplyToMessage.From.ID == b.api.Self.ID
	mentionsBot := false
	if len(message.Entities) > 0 {
		for _, entity := range message.Entities {
			if entity.Type == "mention" {
				mention := message.Text[entity.Offset : entity.Offset+entity.Length]
				if mention == "@"+b.api.Self.UserName {
					mentionsBot = true
					break
				}
			}
		}
	}
	log.Printf("[DEBUG][MH] Chat %d: IsReplyToBot: %t, MentionsBot: %t.", chatID, isReplyToBot, mentionsBot)

	// Отвечаем на прямое обращение к боту
	if isReplyToBot || mentionsBot {
		log.Printf("[DEBUG][MH] Chat %d: Direct mention detected. Sending direct response.", chatID)
		// Запускаем в горутине, чтобы не блокировать основной цикл обработки
		go b.sendDirectResponse(chatID, message)
		log.Printf("[DEBUG][MH] Chat %d: Exiting handleMessage after launching direct response goroutine.", chatID)
		return // После прямого ответа не генерируем обычный
	}

	log.Printf("[DEBUG][MH] Chat %d: No direct mention. Checking conditions for AI response.", chatID)

	// Увеличиваем счетчик сообщений и проверяем, нужно ли отвечать (обычный AI ответ)
	b.settingsMutex.Lock()
	log.Printf("[DEBUG][MH] Chat %d: Settings mutex locked for AI response check.", chatID)
	settings.MessageCount++
	log.Printf("[DEBUG][MH] Chat %d: Message count incremented to %d.", chatID, settings.MessageCount)
	minMsg := settings.MinMessages
	maxMsg := settings.MaxMessages
	msgCount := settings.MessageCount
	isActive := settings.Active
	srachState := settings.SrachState
	shouldReply := false
	if isActive && srachState != "analyzing" && minMsg > 0 {
		targetCount := rand.Intn(maxMsg-minMsg+1) + minMsg
		log.Printf("[DEBUG][MH] Chat %d: Checking AI reply condition: Count(%d) >= Target(%d)?", chatID, msgCount, targetCount)
		if msgCount >= targetCount {
			shouldReply = true
			settings.MessageCount = 0 // Сбрасываем счетчик
			log.Printf("[DEBUG][MH] Chat %d: Reply condition met. Resetting count.", chatID)
		}
	} else {
		log.Printf("[DEBUG][MH] Chat %d: Reply condition not met (Active: %t, SrachState: %s, MinMsg: %d).", chatID, isActive, srachState, minMsg)
	}
	b.settingsMutex.Unlock()
	log.Printf("[DEBUG][MH] Chat %d: Settings mutex unlocked after AI response check. ShouldReply: %t.", chatID, shouldReply)

	// Отвечаем, если нужно
	if shouldReply {
		log.Printf("[DEBUG][MH] Chat %d: Launching AI response goroutine.", chatID)
		go b.sendAIResponse(chatID)
	}

	log.Printf("[DEBUG][MH] Chat %d: Exiting handleMessage normally.", chatID)
	// Проверка на добавление бота в чат остается в handleUpdate
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

// sendAIResponse генерирует и отправляет стандартный AI ответ в фоновом режиме
func (b *Bot) sendAIResponse(chatID int64) {
	if b.config.Debug {
		log.Printf("[DEBUG][MH][Goroutine] Chat %d: Starting AI response generation.", chatID)
	}

	// Получаем историю сообщений ИЗ ХРАНИЛИЩА
	fullHistory := b.storage.GetMessages(chatID)

	// Разделяем историю и последнее сообщение
	var historyForLLM []*tgbotapi.Message
	var lastMessageForLLM *tgbotapi.Message

	if len(fullHistory) > 0 {
		lastMessageIndex := len(fullHistory) - 1
		lastMessageForLLM = fullHistory[lastMessageIndex]
		if lastMessageIndex > 0 {
			historyForLLM = fullHistory[:lastMessageIndex]
		}
	} else {
		// Если история пуста, lastMessageForLLM будет nil, historyForLLM будет пустым срезом
		// Это не должно происходить в sendAIResponse, т.к. она вызывается после добавления сообщения
		log.Printf("[WARN] sendAIResponse: fullHistory пуста для чата %d, хотя сообщение должно было быть добавлено.", chatID)
	}

	log.Printf("[DEBUG][MH][AIResponse] Chat %d: History has %d messages. Last message ID: %d", chatID, len(fullHistory), lastMessageForLLM.MessageID)

	// Берем системный промпт из конфига (используем DefaultPrompt для обычных ответов)
	systemPrompt := b.config.DefaultPrompt
	log.Printf("[DEBUG][MH][AIResponse] Chat %d: Using default system prompt: %s...", chatID, truncateString(systemPrompt, 50))

	// Генерируем ответ, передавая историю и последнее сообщение отдельно
	responseText, err := b.llm.GenerateResponse(systemPrompt, historyForLLM, lastMessageForLLM)
	if err != nil {
		log.Printf("[ERROR][MH][AIResponse] Chat %d: Error generating AI response: %v", chatID, err)
		return
	}
	log.Printf("[DEBUG][MH][AIResponse] Chat %d: AI response generated: %s...", chatID, truncateString(responseText, 50))

	// Отправляем сгенерированный ответ
	msg := tgbotapi.NewMessage(chatID, responseText)
	msg.ParseMode = "Markdown"

	_, err = b.api.Send(msg)
	if err != nil {
		log.Printf("Ошибка отправки AI ответа в чат %d: %v", chatID, err)
	}

	if b.config.Debug {
		log.Printf("[DEBUG][MH][Goroutine] Chat %d: AI response goroutine finished.", chatID)
	}
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
