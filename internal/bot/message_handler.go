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

	// --- Инициализация настроек чата (если нет) ---
	// Этот блок выполняется в handleUpdate перед вызовом handleMessage
	b.settingsMutex.RLock()
	settings, exists := b.chatSettings[chatID]
	if !exists {
		// Если настроек нет даже после handleUpdate, что-то пошло не так
		b.settingsMutex.RUnlock()
		log.Printf("[ERROR] Настройки для чата %d не найдены в handleMessage!", chatID)
		return
	}
	b.settingsMutex.RUnlock()
	// --- Конец инициализации (проверки) ---

	// --- Обработка отмены ввода ---
	b.settingsMutex.RLock()
	currentPendingSetting := settings.PendingSetting
	b.settingsMutex.RUnlock()
	if text == "/cancel" && currentPendingSetting != "" {
		b.settingsMutex.Lock()
		settings.PendingSetting = "" // Сбрасываем ожидание
		b.settingsMutex.Unlock()
		b.sendReply(chatID, "Ввод отменен.")
		b.sendSettingsKeyboard(chatID) // Показываем меню настроек снова
		return
	}
	// --- Конец обработки отмены ---

	// --- Обработка ввода ожидаемой настройки ---
	if currentPendingSetting != "" {
		isValidInput := false
		parsedValue, err := strconv.Atoi(text)

		if err != nil {
			b.sendReply(chatID, "Ошибка: Введите числовое значение или /cancel для отмены.")
		} else {
			b.settingsMutex.Lock() // Блокируем для изменения настроек
			switch currentPendingSetting {
			case "min_messages":
				if parsedValue > 0 && parsedValue <= settings.MaxMessages {
					settings.MinMessages = parsedValue
					isValidInput = true
				} else {
					b.sendReply(chatID, fmt.Sprintf("Ошибка: Минимальное значение должно быть больше 0 и не больше максимального (%d).", settings.MaxMessages))
				}
			case "max_messages":
				if parsedValue >= settings.MinMessages {
					settings.MaxMessages = parsedValue
					isValidInput = true
				} else {
					b.sendReply(chatID, fmt.Sprintf("Ошибка: Максимальное значение должно быть не меньше минимального (%d).", settings.MinMessages))
				}
			case "daily_time":
				if parsedValue >= 0 && parsedValue <= 23 {
					settings.DailyTakeTime = parsedValue
					log.Printf("Настройка времени тейка изменена на %d для чата %d. Перезапустите бота для применения ко всем чатам или реализуйте динамическое обновление.", parsedValue, chatID)
					isValidInput = true
				} else {
					b.sendReply(chatID, "Ошибка: Введите час от 0 до 23.")
				}
			case "summary_interval":
				if parsedValue >= 0 { // 0 - выключено
					settings.SummaryIntervalHours = parsedValue
					settings.LastAutoSummaryTime = time.Time{} // Сбрасываем таймер при изменении интервала
					log.Printf("Интервал авто-саммари для чата %d изменен на %d ч.", chatID, parsedValue)
					isValidInput = true
				} else {
					b.sendReply(chatID, "Ошибка: Интервал не может быть отрицательным (0 - выключить).")
				}
			}

			if isValidInput {
				settings.PendingSetting = "" // Сбрасываем ожидание после успешного ввода
				b.sendReply(chatID, "Настройка успешно обновлена!")
			}
			b.settingsMutex.Unlock() // Разблокируем после изменения

			if isValidInput {
				b.sendSettingsKeyboard(chatID) // Показываем обновленное меню
			}
		}
		return // Прекращаем дальнейшую обработку сообщения, т.к. это был ввод настройки
	}
	// --- Конец обработки ввода ---

	// Команды обрабатываются в handleUpdate перед вызовом handleMessage

	// --- Логика Анализа Срачей ---
	b.settingsMutex.RLock()
	srachEnabled := settings.SrachAnalysisEnabled
	b.settingsMutex.RUnlock()

	srachHandled := false // Флаг, что сообщение обработано логикой срача
	if srachEnabled {
		isPotentialSrachMsg := b.isPotentialSrachTrigger(message) // Передаем *tgbotapi.Message

		b.settingsMutex.Lock()
		currentState := settings.SrachState
		lastTriggerTime := settings.LastSrachTriggerTime

		if isPotentialSrachMsg {
			if settings.SrachState == "none" {
				settings.SrachState = "detected"
				settings.SrachStartTime = messageTime
				settings.SrachMessages = []string{formatMessageForAnalysis(message)} // Передаем *tgbotapi.Message
				settings.LastSrachTriggerTime = messageTime
				settings.SrachLlmCheckCounter = 0
				b.settingsMutex.Unlock() // Разблокируем перед отправкой
				b.sendSrachWarning(chatID)
				log.Printf("Чат %d: Обнаружен потенциальный срач.", chatID)
				srachHandled = true // Считаем сообщение обработанным (начало срача)
			} else if settings.SrachState == "detected" {
				settings.SrachMessages = append(settings.SrachMessages, formatMessageForAnalysis(message)) // Передаем *tgbotapi.Message
				settings.LastSrachTriggerTime = messageTime
				settings.SrachLlmCheckCounter++

				const llmCheckInterval = 3
				if settings.SrachLlmCheckCounter%llmCheckInterval == 0 {
					msgTextToCheck := message.Text
					go func() {
						isConfirmed := b.confirmSrachWithLLM(chatID, msgTextToCheck)
						log.Printf("[LLM Srach Confirm] Чат %d: Сообщение ID %d. Результат LLM: %t", chatID, message.MessageID, isConfirmed)
					}()
				}
				b.settingsMutex.Unlock() // Разблокируем
				srachHandled = true      // Считаем сообщение обработанным (продолжение срача)
			} else {
				b.settingsMutex.Unlock() // Не меняем состояние, разблокируем
			}
		} else if currentState == "detected" {
			// Сообщение не триггер, но срач был активен
			settings.SrachMessages = append(settings.SrachMessages, formatMessageForAnalysis(message)) // Передаем *tgbotapi.Message

			// Проверка на завершение срача по таймеру
			const srachTimeout = 5 * time.Minute
			if !lastTriggerTime.IsZero() && messageTime.Sub(lastTriggerTime) > srachTimeout {
				log.Printf("Чат %d: Срач считается завершенным по тайм-ауту (%v).", chatID, srachTimeout)
				b.settingsMutex.Unlock() // Разблокируем перед анализом
				go b.analyseSrach(chatID)
				srachHandled = true // Сообщение обработано (последнее перед анализом)
			} else {
				b.settingsMutex.Unlock() // Просто добавили сообщение, разблокируем
				srachHandled = true      // Считаем обработанным (часть активного срача)
			}
		} else {
			b.settingsMutex.Unlock() // Срач не активен и сообщение не триггер
		}
	}
	// --- Конец Логики Анализа Срачей ---

	// Если сообщение было частью срача (начало, продолжение, конец),
	// то дальнейшую обработку (прямой ответ, AI ответ) пропускаем.
	if srachHandled {
		return
	}

	// --- Обработка обычных сообщений (не срач, не ввод настроек, не команда) ---

	// Проверяем, является ли сообщение ответом на сообщение бота или обращением к боту
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

	// Отвечаем на прямое обращение к боту
	if isReplyToBot || mentionsBot {
		log.Printf("Обнаружено прямое обращение к боту в чате %d, отправляю ответ", chatID)
		go b.sendDirectResponse(chatID, message)
		return // После прямого ответа не генерируем обычный
	}

	// Увеличиваем счетчик сообщений и проверяем, нужно ли отвечать (обычный AI ответ)
	b.settingsMutex.Lock()
	settings.MessageCount++
	// Проверяем, активен ли бот и не идет ли анализ срача (дополнительная проверка)
	shouldReply := settings.Active && settings.SrachState != "analyzing" && settings.MinMessages > 0 && settings.MessageCount >= rand.Intn(settings.MaxMessages-settings.MinMessages+1)+settings.MinMessages
	if shouldReply {
		settings.MessageCount = 0 // Сбрасываем счетчик
	}
	b.settingsMutex.Unlock()

	// Отвечаем, если нужно
	if shouldReply {
		go b.sendAIResponse(chatID)
	}

	// Проверка на добавление бота в чат остается в handleUpdate
}

// sendDirectResponse отправляет ответ на прямое обращение к боту
func (b *Bot) sendDirectResponse(chatID int64, message *tgbotapi.Message) {
	if b.config.Debug {
		log.Printf("[DEBUG] Отправка прямого ответа в чат %d на сообщение от %s (%s)",
			chatID, message.From.FirstName, message.From.UserName)
	}

	// Получаем некоторый контекст из истории
	messages := b.storage.GetMessages(chatID)

	// Для прямого ответа используем только DIRECT_PROMPT
	response, err := b.llm.GenerateResponse(b.config.DirectPrompt, messages)
	if err != nil {
		if b.config.Debug {
			log.Printf("[DEBUG] Ошибка при генерации прямого ответа: %v. Полный текст ошибки: %s", err, err.Error())
			log.Printf("[DEBUG] LLM Provider: %s", b.config.LLMProvider)
			log.Printf("[DEBUG] Промпт для прямого ответа: %s", b.config.DirectPrompt)
			log.Printf("[DEBUG] Количество сообщений в контексте: %d", len(messages))
		} else {
			log.Printf("Ошибка при генерации прямого ответа: %v", err)
		}
		return
	}

	// Создаем сообщение с ответом на исходное сообщение
	msg := tgbotapi.NewMessage(chatID, response)
	msg.ParseMode = "Markdown"
	msg.ReplyToMessageID = message.MessageID

	_, err = b.api.Send(msg)
	if err != nil {
		if b.config.Debug {
			log.Printf("[DEBUG] Ошибка отправки сообщения: %v. Полный текст ошибки: %s", err, err.Error())
		} else {
			log.Printf("Ошибка отправки сообщения: %v", err)
		}
	} else if b.config.Debug {
		log.Printf("[DEBUG] Успешно отправлен прямой ответ в чат %d", chatID)
	}
}

// sendAIResponse генерирует и отправляет ответ нейросети
func (b *Bot) sendAIResponse(chatID int64) {
	if b.config.Debug {
		log.Printf("[DEBUG] Генерация AI ответа для чата %d", chatID)
	}

	// Получаем историю сообщений
	messages := b.storage.GetMessages(chatID)
	if len(messages) == 0 {
		if b.config.Debug {
			log.Printf("[DEBUG] Нет сообщений для чата %d, ответ не отправлен", chatID)
		}
		return
	}

	// Получаем настройки промпта
	b.settingsMutex.RLock()
	settings, exists := b.chatSettings[chatID]
	prompt := b.config.DefaultPrompt
	if exists && settings.CustomPrompt != "" {
		prompt = settings.CustomPrompt
	}
	b.settingsMutex.RUnlock()

	if b.config.Debug {
		log.Printf("[DEBUG] Используется промпт: %s", prompt[:min(30, len(prompt))]+"...")
		log.Printf("[DEBUG] Количество сообщений в контексте: %d", len(messages))
	}

	// Отправляем запрос к Gemini
	response, err := b.llm.GenerateResponse(prompt, messages)
	if err != nil {
		if b.config.Debug {
			log.Printf("[DEBUG] Ошибка при генерации ответа: %v. Полный текст ошибки: %s", err, err.Error())
			log.Printf("[DEBUG] LLM Provider: %s", b.config.LLMProvider)
		} else {
			log.Printf("Ошибка при генерации ответа: %v", err)
		}
		return
	}

	// Отправляем ответ в чат
	b.sendReply(chatID, response)

	if b.config.Debug {
		log.Printf("[DEBUG] Успешно отправлен AI ответ в чат %d", chatID)
	}
}
