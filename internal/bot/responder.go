package bot

import (
	"context"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	// Для UserProfile
	"github.com/Henry-Case-dev/rofloslav/internal/utils" // Для TruncateString
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// === Функции для отправки ответов (перенесены из message_handler.go) ===

// sendDirectResponse обрабатывает прямое упоминание или ответ на сообщение бота
func (b *Bot) sendDirectResponse(chatID int64, message *tgbotapi.Message) {
	startTime := time.Now()
	defer func() {
		log.Printf("[DEBUG][Timing] Генерация DirectResponse (ReplyID: %d) для чата %d заняла %s",
			message.MessageID, chatID, time.Since(startTime))
	}()

	if message == nil {
		log.Printf("[ERROR][DR] Chat %d: Невозможно отправить прямой ответ, сообщение nil", chatID)
		return
	}

	log.Printf("[INFO][DR] Chat %d: Получен прямой запрос от %s (ID: %d)", chatID, message.From.UserName, message.From.ID)

	// Проверяем наличие фотографии в сообщении
	hasPhoto := message.Photo != nil && len(message.Photo) > 0

	// Если в сообщении есть фотография, сначала обрабатываем её чтобы сохранить описание в хранилище
	photoDescription := ""
	if hasPhoto {
		log.Printf("[INFO][DR] Chat %d: Обнаружена фотография в прямом обращении", chatID)

		// Получаем настройки чата для проверки включения анализа фото
		settings, err := b.storage.GetChatSettings(chatID)
		photoAnalysisEnabled := b.config.PhotoAnalysisEnabled
		if err == nil && settings != nil && settings.PhotoAnalysisEnabled != nil {
			photoAnalysisEnabled = *settings.PhotoAnalysisEnabled
		}

		// Анализируем изображение только если включен PhotoAnalysisEnabled
		if photoAnalysisEnabled && b.embeddingClient != nil && b.config.GeminiAPIKey != "" {
			// Получаем самую большую фотографию (последнюю в массиве)
			photoSize := message.Photo[len(message.Photo)-1]

			// Получаем информацию о файле
			fileConfig := tgbotapi.FileConfig{
				FileID: photoSize.FileID,
			}
			file, err := b.api.GetFile(fileConfig)
			if err != nil {
				log.Printf("[ERROR][DR] Chat %d: Не удалось получить информацию о фото: %v", chatID, err)
			} else {
				// Загружаем файл
				fileURL := fmt.Sprintf("https://api.telegram.org/file/bot%s/%s", b.api.Token, file.FilePath)
				client := &http.Client{}
				resp, err := client.Get(fileURL)
				if err != nil {
					log.Printf("[ERROR][DR] Chat %d: Не удалось загрузить фото: %v", chatID, err)
				} else {
					defer resp.Body.Close()

					// Читаем содержимое файла
					photoFileBytes, err := io.ReadAll(resp.Body)
					if err != nil {
						log.Printf("[ERROR][DR] Chat %d: Не удалось прочитать содержимое фото: %v", chatID, err)
					} else {
						// Анализируем фото с помощью Gemini
						photoDescription, err = b.analyzeImageWithGemini(context.Background(), photoFileBytes, message.Caption)
						if err != nil {
							log.Printf("[ERROR][DR] Chat %d: Ошибка при анализе фото: %v", chatID, err)
						} else {
							log.Printf("[INFO][DR] Chat %d: Успешно проанализирована фотография в прямом обращении", chatID)

							// Сохраняем текстовое описание изображения в хранилище
							textMessage := &tgbotapi.Message{
								MessageID: message.MessageID,
								From:      message.From,
								Chat:      message.Chat,
								Date:      message.Date,
								Text:      "[Анализ изображения]: " + photoDescription,
							}
							b.storage.AddMessage(chatID, textMessage)
						}
					}
				}
			}
		}
	}

	// Получаем последние сообщения из чата для контекста
	messages, err := b.storage.GetMessages(chatID, b.config.ContextWindow)
	if err != nil {
		log.Printf("[ERROR][DR] Chat %d: Ошибка при получении истории сообщений: %v", chatID, err)
		// Отправляем ошибку пользователю
		errMsg := tgbotapi.NewMessage(chatID, "⚠️ Извините, произошла ошибка при обработке вашего запроса.")
		errMsg.ReplyToMessageID = message.MessageID
		b.api.Send(errMsg)
		return
	}

	// Получаем цепочку сообщений, на которые отвечали
	var replyChain []*tgbotapi.Message
	if message.ReplyToMessage != nil {
		// Ограничиваем глубину цепочки ответов до 5
		replyChain, err = b.storage.GetReplyChain(context.Background(), chatID, message.ReplyToMessage.MessageID, 5)
		if err != nil {
			log.Printf("[WARN][DR] Chat %d: Ошибка при получении цепочки ответов: %v", chatID, err)
			// Продолжаем работу даже если не удалось получить цепочку ответов
		}
	}

	// Получаем релевантные сообщения с использованием долгосрочной памяти, если она включена
	var relevantMessages []*tgbotapi.Message
	if b.config.LongTermMemoryEnabled {
		// Объединяем текст сообщения с описанием фотографии, если оно есть
		queryText := message.Text
		if hasPhoto && photoDescription != "" {
			if queryText != "" {
				queryText += "\n\n" + photoDescription
			} else {
				queryText = photoDescription
			}
		}

		// Если текст пустой (например, только фото без подписи), используем базовый текст
		if queryText == "" {
			queryText = "фотография"
		}

		// Ищем релевантные сообщения
		relevantMsgs, err := b.storage.SearchRelevantMessages(chatID, queryText, b.config.LongTermMemoryFetchK)
		if err != nil {
			log.Printf("[WARN][DR] Chat %d: Ошибка при поиске релевантных сообщений: %v", chatID, err)
		} else {
			relevantMessages = relevantMsgs
			if b.config.Debug {
				log.Printf("[DEBUG][DR] Chat %d: Найдено %d релевантных сообщений", chatID, len(relevantMessages))
			}
		}
	}

	// Форматируем контекст
	formattedContext := formatDirectReplyContext(chatID, message, replyChain, messages, relevantMessages, b.storage, b.config, b.config.TimeZone)

	// Если в сообщении есть фотография, добавляем информацию о ней в контекст
	if hasPhoto && photoDescription != "" {
		// Добавляем описание фотографии в контекст
		userInfo := fmt.Sprintf("Пользователь %s прикрепил к сообщению фотографию", message.From.UserName)
		if message.Caption != "" {
			userInfo += fmt.Sprintf(" с подписью: \"%s\"", message.Caption)
		}
		photoInfo := fmt.Sprintf("%s\nОписание фотографии: %s", userInfo, photoDescription)

		// Добавляем информацию о фотографии в начало контекста
		formattedContext = photoInfo + "\n\n" + formattedContext
	}

	// Начинаем анализ типа сообщения - серьезное или обычное
	msgType := "regular"

	// Классифицируем только если есть не-пустой текст или если есть фотография с описанием
	if message.Text != "" || (hasPhoto && photoDescription != "") {
		// Формируем входной текст для классификации, объединяя текст сообщения и описание фотографии
		classifyInput := message.Text
		if hasPhoto && photoDescription != "" {
			if classifyInput != "" {
				classifyInput += "\n\n[Содержимое фотографии]: " + photoDescription
			} else {
				classifyInput = "[Содержимое фотографии]: " + photoDescription
			}
		}

		if b.config.ClassifyDirectMessagePrompt != "" {
			// Классифицируем сообщение
			classifyResult, err := b.llm.GenerateArbitraryResponse(
				b.config.ClassifyDirectMessagePrompt,
				classifyInput,
			)
			if err != nil {
				log.Printf("[WARN][DR] Chat %d: Ошибка при классификации сообщения: %v", chatID, err)
			} else {
				// Проверяем результат классификации
				lower := strings.ToLower(strings.TrimSpace(classifyResult))
				if strings.Contains(lower, "serious") {
					msgType = "serious"
					log.Printf("[DEBUG][DR] Chat %d: Сообщение классифицировано как SERIOUS", chatID)
				} else {
					log.Printf("[DEBUG][DR] Chat %d: Сообщение классифицировано как REGULAR", chatID)
				}
			}
		}
	}

	// Выбираем промпт в зависимости от типа сообщения
	var responsePrompt string
	if msgType == "serious" && b.config.SeriousDirectPrompt != "" {
		responsePrompt = b.config.SeriousDirectPrompt
		log.Printf("[INFO][DR] Chat %d: Используем SERIOUS_DIRECT_PROMPT", chatID)
	} else {
		responsePrompt = b.config.DirectPrompt
		log.Printf("[INFO][DR] Chat %d: Используем стандартный DIRECT_PROMPT", chatID)
	}

	// Генерируем ответ
	responseText, err := b.llm.GenerateResponseFromTextContext(responsePrompt, formattedContext)
	if err != nil {
		log.Printf("[ERROR][DR] Chat %d: Ошибка при генерации ответа: %v", chatID, err)
		// Отправляем ошибку пользователю
		errMsg := tgbotapi.NewMessage(chatID, "⚠️ Извините, возникла проблема при генерации ответа.")
		errMsg.ReplyToMessageID = message.MessageID
		b.api.Send(errMsg)
		return
	}

	// Ограничиваем длину ответа для Telegram
	if len(responseText) > 4096 {
		responseText = responseText[:4093] + "..."
	}

	// Отправляем ответ
	msg := tgbotapi.NewMessage(chatID, responseText)
	msg.ReplyToMessageID = message.MessageID
	_, err = b.api.Send(msg)
	if err != nil {
		log.Printf("[ERROR][DR] Chat %d: Ошибка при отправке сообщения: %v", chatID, err)
	}
}

// sendAIResponse генерирует и отправляет обычный AI ответ в чат
func (b *Bot) sendAIResponse(chatID int64) {
	// 1. Получаем общий контекст (недавние сообщения)
	commonContextMessages, err := b.storage.GetMessages(chatID, b.config.ContextWindow)
	if err != nil {
		log.Printf("[ERROR][sendAIResponse] Ошибка получения общего контекста для чата %d: %v", chatID, err)
		return
	}

	if len(commonContextMessages) == 0 {
		log.Printf("[WARN][sendAIResponse] Нет сообщений для генерации ответа в чате %d", chatID)
		return
	}

	// 2. Формируем запрос и выполняем поиск RAG (если включено)
	var relevantMessages []*tgbotapi.Message
	var errRAG error
	if b.config.LongTermMemoryEnabled {
		// Формируем запрос из текста последних N сообщений
		numMessagesForQuery := 5 // Количество последних сообщений для запроса RAG (можно вынести в конфиг)
		startIndex := len(commonContextMessages) - numMessagesForQuery
		if startIndex < 0 {
			startIndex = 0
		}
		var queryBuilder strings.Builder
		for _, msg := range commonContextMessages[startIndex:] {
			queryText := msg.Text
			if queryText == "" {
				queryText = msg.Caption
			}
			if queryText != "" {
				queryBuilder.WriteString(queryText)
				queryBuilder.WriteString("\n") // Разделяем тексты сообщений новой строкой
			}
		}
		ragQuery := strings.TrimSpace(queryBuilder.String())

		if ragQuery != "" {
			log.Printf("[DEBUG][sendAIResponse] Chat %d: Searching relevant messages (RAG) for query based on last %d messages: '%s...'", chatID, numMessagesForQuery, utils.TruncateString(ragQuery, 100))
			relevantMessages, errRAG = b.storage.SearchRelevantMessages(chatID, ragQuery, b.config.LongTermMemoryFetchK)
			if errRAG != nil {
				log.Printf("[ERROR][sendAIResponse] Chat %d: Ошибка RAG поиска: %v", chatID, errRAG)
				relevantMessages = nil
			} else {
				log.Printf("[DEBUG][sendAIResponse] Chat %d: Found %d relevant messages (RAG).", chatID, len(relevantMessages))
			}
		} else {
			log.Printf("[DEBUG][sendAIResponse] Chat %d: RAG query text is empty, skipping search.", chatID)
		}
	}

	// 3. Формируем финальный структурированный контекст
	log.Printf("[DEBUG][sendAIResponse] Chat %d: Formatting combined context...", chatID)
	contextText := formatDirectReplyContext(chatID,
		nil,
		nil,
		commonContextMessages,
		relevantMessages,
		b.storage,
		b.config,
		b.config.TimeZone,
	)

	if contextText == "" {
		log.Printf("[WARN][sendAIResponse] Чат %d: Финальный контекст для AI ответа пуст.", chatID)
		return
	}

	if b.config.Debug {
		log.Printf("[DEBUG][sendAIResponse] Chat %d: Final context for LLM:\n%s", chatID, contextText) // Логируем весь контекст в дебаге
	}

	// 4. Используем основной промпт и генерируем ответ
	systemPrompt := b.config.DefaultPrompt
	response, err := b.llm.GenerateResponseFromTextContext(systemPrompt, contextText)
	if err != nil {
		log.Printf("[ERROR][sendAIResponse] Ошибка генерации AI ответа для чата %d: %v", chatID, err)
		return
	}

	// 5. Отправляем ответ
	b.sendReply(chatID, response)
}

// getDirectReplyLimitSettings читает настройки лимита из хранилища или конфига
func (b *Bot) getDirectReplyLimitSettings(chatID int64) (enabled bool, count int, duration time.Duration) {
	b.settingsMutex.RLock() // Только читаем
	defer b.settingsMutex.RUnlock()

	dbSettings, errDb := b.storage.GetChatSettings(chatID)
	if errDb != nil {
		log.Printf("[WARN][getDirectReplyLimitSettings] Chat %d: Ошибка получения настроек из БД: %v. Использую дефолтные.", chatID, errDb)
		dbSettings = nil
	}

	enabled = b.config.DirectReplyLimitEnabledDefault
	count = b.config.DirectReplyLimitCountDefault
	duration = b.config.DirectReplyLimitDurationDefault

	if dbSettings != nil {
		if dbSettings.DirectReplyLimitEnabled != nil {
			enabled = *dbSettings.DirectReplyLimitEnabled
		}
		if dbSettings.DirectReplyLimitCount != nil {
			count = *dbSettings.DirectReplyLimitCount
		}
		if dbSettings.DirectReplyLimitDuration != nil {
			duration = time.Duration(*dbSettings.DirectReplyLimitDuration) * time.Minute
		}
	}
	return
}

// checkDirectReplyLimit проверяет, превышен ли лимит прямых обращений к боту для пользователя.
// Возвращает true, если лимит ПРЕВЫШЕН.
// Переписано для корректной и безопасной работы с мьютексом и картой.
func (b *Bot) checkDirectReplyLimit(chatID int64, userID int64) bool {
	// Получаем актуальные настройки из хранилища
	settings, err := b.storage.GetChatSettings(chatID)
	if err != nil {
		log.Printf("[ERROR][checkDirectReplyLimit] Не удалось получить настройки чата %d: %v. Пропускаю проверку.", chatID, err)
		return false // Считаем, что лимит не превышен
	}

	// Проверяем, включен ли лимит
	limitEnabled := b.config.DirectReplyLimitEnabledDefault
	if settings != nil && settings.DirectReplyLimitEnabled != nil {
		limitEnabled = *settings.DirectReplyLimitEnabled
	}
	if !limitEnabled {
		return false // Лимит выключен
	}

	// Получаем значения лимита
	limitCount := b.config.DirectReplyLimitCountDefault
	if settings != nil && settings.DirectReplyLimitCount != nil {
		limitCount = *settings.DirectReplyLimitCount
	}
	limitDuration := b.config.DirectReplyLimitDurationDefault
	if settings != nil && settings.DirectReplyLimitDuration != nil {
		limitDuration = time.Duration(*settings.DirectReplyLimitDuration) * time.Minute
	}

	// Используем полную блокировку для безопасного чтения и возможной записи
	b.settingsMutex.Lock()
	defer b.settingsMutex.Unlock() // Гарантируем разблокировку

	// --- Ensure the map for the chatID exists ---
	if _, ok := b.directReplyTimestamps[chatID]; !ok {
		b.directReplyTimestamps[chatID] = make(map[int64][]time.Time)
		if b.config.Debug {
			log.Printf("[DEBUG][RateLimit] Initialized directReplyTimestamps map for chat %d", chatID)
		}
	}
	// --- End ensure map exists ---

	// Now it's safe to access b.directReplyTimestamps[chatID]
	userTimestamps := b.directReplyTimestamps[chatID][userID] // Read timestamps for the user

	// Clean up timestamps older than the duration
	now := time.Now()
	cutoff := now.Add(-limitDuration)
	cleanedTimestamps := make([]time.Time, 0, len(userTimestamps))
	for _, ts := range userTimestamps {
		if ts.After(cutoff) {
			cleanedTimestamps = append(cleanedTimestamps, ts)
		}
	}
	userTimestamps = cleanedTimestamps // Update userTimestamps with cleaned slice

	// Check limit BEFORE adding the new timestamp
	if len(userTimestamps) >= limitCount {
		log.Printf("[INFO][RateLimit] Chat %d, User %d: Direct reply limit exceeded (%d/%d in %v)", chatID, userID, len(userTimestamps), limitCount, limitDuration)
		// Don't add the timestamp if the limit is already hit
		return false // Limit exceeded
	}

	// Append the new timestamp to the cleaned slice (limit not exceeded)
	b.directReplyTimestamps[chatID][userID] = append(userTimestamps, now)

	if b.config.Debug {
		log.Printf("[DEBUG][RateLimit] Chat %d, User %d: Timestamp added. Count: %d/%d", chatID, userID, len(b.directReplyTimestamps[chatID][userID]), limitCount)
	}
	return true // Limit not exceeded
}

// sendDirectLimitExceededReply отправляет сообщение о превышении лимита прямых обращений.
func (b *Bot) sendDirectLimitExceededReply(chatID int64, replyToMessageID int) {
	limitPrompt := b.config.DirectReplyLimitPrompt
	limitMsgText := "🚫 " // Префикс по умолчанию

	// Генерируем кастомное сообщение с помощью LLM
	generatedText, err := b.llm.GenerateArbitraryResponse(limitPrompt, "")
	if err != nil {
		log.Printf("[ERROR][DirectLimit] Ошибка генерации сообщения о лимите для чата %d: %v", chatID, err)
		// Используем простой текст, если генерация не удалась
		limitMsgText += "Слишком часто обращаешься! Отдохни."
	} else {
		limitMsgText += generatedText // Используем сгенерированный текст
	}

	msg := tgbotapi.NewMessage(chatID, limitMsgText)
	msg.ReplyToMessageID = replyToMessageID

	_, errSend := b.api.Send(msg)
	if errSend != nil {
		log.Printf("[ERROR][DirectLimit] Ошибка отправки сообщения о лимите в чат %d: %v", chatID, errSend)
	}
}

// sendErrorReply отправляет стандартизированное сообщение об ошибке в чат.
func (b *Bot) sendErrorReply(chatID int64, replyToMessageID int, errorContext string) {
	// Логируем детальную ошибку перед отправкой общего сообщения
	log.Printf("[ERROR] Подробности ошибки для чата %d (ReplyTo: %d): %s", chatID, replyToMessageID, errorContext)

	errorMsg := "⚠️ Извините, возникла проблема при генерации ответа."
	// Если включен режим отладки, добавляем контекст ошибки в сообщение
	if b.config.Debug {
		errorMsg = fmt.Sprintf("⚠️ Ошибка (%s)", errorContext)
	}

	msg := tgbotapi.NewMessage(chatID, errorMsg)
	msg.ReplyToMessageID = replyToMessageID
	_, err := b.api.Send(msg)
	if err != nil {
		// Логируем, если не удалось отправить даже сообщение об ошибке
		log.Printf("[CRITICAL] НЕ УДАЛОСЬ отправить сообщение об ошибке в чат %d: %v", chatID, err)
	}
}
