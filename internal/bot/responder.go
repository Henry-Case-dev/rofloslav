package bot

import (
	"context"
	"log"
	"strings"
	"time"

	// Для UserProfile
	"github.com/Henry-Case-dev/rofloslav/internal/utils" // Для TruncateString
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// === Функции для отправки ответов (перенесены из message_handler.go) ===

// sendDirectResponse обрабатывает прямое упоминание или ответ на сообщение бота
func (b *Bot) sendDirectResponse(chatID int64, message *tgbotapi.Message) {
	// --- Получение данных для контекста ---
	ctx := context.Background() // Используем общий контекст для запросов к хранилищу

	// 1. Релевантные сообщения из долгосрочной памяти (RAG)
	var relevantMessages []*tgbotapi.Message
	var errRAG error
	if b.config.LongTermMemoryEnabled && message.Text != "" { // Ищем только если включено и есть текст
		log.Printf("[DEBUG][sendDirectResponse] Chat %d: Searching relevant messages (RAG) for query: '%s'", chatID, message.Text)
		// Контекст не передается в SearchRelevantMessages по интерфейсу
		// ragCtx, ragCancel := context.WithTimeout(ctx, 10*time.Second)
		relevantMessages, errRAG = b.storage.SearchRelevantMessages(chatID, message.Text, b.config.LongTermMemoryFetchK) // << Без контекста
		// ragCancel()
		if errRAG != nil {
			log.Printf("[ERROR][sendDirectResponse] Chat %d: Ошибка RAG поиска: %v", chatID, errRAG)
			relevantMessages = nil
		} else {
			log.Printf("[DEBUG][sendDirectResponse] Chat %d: Found %d relevant messages (RAG).", chatID, len(relevantMessages))
		}
	}

	// 2. История сообщений (общий контекст)
	// Контекст не передается в GetMessages по интерфейсу
	// commonCtx, commonCancel := context.WithTimeout(ctx, 5*time.Second)
	commonContextMessages, errCommon := b.storage.GetMessages(chatID, b.config.ContextWindow) // << Без контекста
	// commonCancel()
	if errCommon != nil {
		log.Printf("[ERROR][sendDirectResponse] Ошибка получения общего контекста для чата %d: %v", chatID, errCommon)
		b.sendReply(chatID, "Не могу получить историю, чтобы ответить.")
		return
	}

	// 3. Ветка ответов (Reply Chain)
	var replyChainMessages []*tgbotapi.Message
	var errChain error
	// Определяем стартовое сообщение для цепочки
	startMsgID := message.MessageID
	// Максимальная глубина цепочки (можно вынести в конфиг)
	maxChainDepth := 15
	log.Printf("[DEBUG][sendDirectResponse] Chat %d: Fetching reply chain starting from message %d (max depth %d).", chatID, startMsgID, maxChainDepth)
	// Используем контекст с таймаутом
	chainCtx, chainCancel := context.WithTimeout(ctx, 15*time.Second) // Больший таймаут для цепочки
	replyChainMessages, errChain = b.storage.GetReplyChain(chainCtx, chatID, startMsgID, maxChainDepth)
	chainCancel()
	if errChain != nil {
		log.Printf("[ERROR][sendDirectResponse] Chat %d: Ошибка получения ветки ответов для сообщения %d: %v", chatID, startMsgID, errChain)
		// Не критично, продолжим без ветки
		replyChainMessages = nil
	} else {
		log.Printf("[DEBUG][sendDirectResponse] Chat %d: Fetched %d messages in reply chain.", chatID, len(replyChainMessages))
	}

	// --- Формирование финального контекста ---
	log.Printf("[DEBUG][sendDirectResponse] Chat %d: Formatting combined context...", chatID)
	contextText := formatDirectReplyContext(
		chatID,
		replyChainMessages,    // Ветка ответов
		commonContextMessages, // Общий контекст
		relevantMessages,      // RAG
		b.storage,
		b.config,
		b.config.TimeZone,
	)

	if contextText == "" {
		log.Printf("[WARN][sendDirectResponse] Чат %d: Финальный контекст для прямого ответа пуст.", chatID)
		b.sendReply(chatID, "Не могу сформировать контекст для ответа.")
		return
	}

	if b.config.Debug {
		log.Printf("[DEBUG][sendDirectResponse] Chat %d: Final context for LLM:\n%s", chatID, contextText) // Логируем весь контекст в дебаге
	}

	// Определяем, какой промпт использовать (обычный прямой или серьезный)
	finalPrompt := b.config.DirectPrompt // Промпт по умолчанию
	classification := "casual"           // Предполагаем несерьезный ответ

	// Классификация сообщения (если есть текст)
	if message.Text != "" {
		classifyPrompt := b.config.ClassifyDirectMessagePrompt + "\n\n" + message.Text
		llmResponse, classifyErr := b.llm.GenerateArbitraryResponse(classifyPrompt, "")
		if classifyErr != nil {
			log.Printf("[WARN][sendDirectResponse] Chat %d: Ошибка классификации сообщения: %v", chatID, classifyErr)
			// Продолжаем с 'casual' по умолчанию
		} else {
			classification = strings.ToLower(strings.TrimSpace(llmResponse))
			if b.config.Debug {
				log.Printf("[DEBUG][sendDirectResponse] Chat %d: Классификация прямого ответа: '%s'", chatID, classification)
			}
		}
	}

	// Выбираем финальный промпт
	if classification == "serious" {
		finalPrompt = b.config.SeriousDirectPrompt
	} // Иначе остается DirectPrompt

	if b.config.Debug {
		log.Printf("[DEBUG][sendDirectResponse] Chat %d: Используется финальный промпт: %s...", chatID, utils.TruncateString(finalPrompt, 150))
	}

	// Генерируем ответ LLM, используя НОВЫЙ contextText
	response, err := b.llm.GenerateResponseFromTextContext(finalPrompt, contextText)
	if err != nil {
		log.Printf("[ERROR][sendDirectResponse] Ошибка генерации прямого ответа для чата %d: %v", chatID, err)
		b.sendReply(chatID, "Не могу придумать ответ.")
		return
	}

	// ОТПРАВЛЯЕМ СООБЩЕНИЕ КАК ОТВЕТ
	msg := tgbotapi.NewMessage(chatID, response)
	msg.ReplyToMessageID = message.MessageID // Устанавливаем ID сообщения для ответа
	_, errSend := b.api.Send(msg)
	if errSend != nil {
		log.Printf("[ERROR][sendDirectResponse] Ошибка отправки прямого ответа (как reply) в чат %d: %v", chatID, errSend)
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
	contextText := formatDirectReplyContext( // Используем ту же функцию форматирования
		chatID,
		nil,                   // Нет ветки ответов для интервального сообщения
		commonContextMessages, // Общий контекст
		relevantMessages,      // RAG
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

// checkAndRecordDirectReply проверяет лимит и записывает метку времени, если лимит не превышен.
// Возвращает true, если лимит превышен, иначе false.
func (b *Bot) checkAndRecordDirectReply(chatID int64, userID int64) bool {
	enabled, limitCount, limitDuration := b.getDirectReplyLimitSettings(chatID)

	// Если лимит выключен для чата ИЛИ count некорректен (<=0), сразу выходим
	if !enabled || limitCount <= 0 {
		if b.config.Debug {
			log.Printf("[Direct Limit Check] Chat %d, User %d: Limit is disabled (Enabled: %t, Count: %d). Returning false (not exceeded).", chatID, userID, enabled, limitCount)
		}
		return false // Лимит не превышен
	}

	b.settingsMutex.Lock() // Используем Lock для чтения и записи
	defer b.settingsMutex.Unlock()

	now := time.Now()
	limitWindowStart := now.Add(-limitDuration)

	// Инициализируем map'ы, если их нет
	if b.directReplyTimestamps == nil {
		b.directReplyTimestamps = make(map[int64]map[int64][]time.Time)
	}
	if b.directReplyTimestamps[chatID] == nil {
		b.directReplyTimestamps[chatID] = make(map[int64][]time.Time)
	}
	userTimestamps := b.directReplyTimestamps[chatID][userID]

	// Фильтруем метки, оставляем только те, что в пределах окна
	validTimestamps := make([]time.Time, 0, len(userTimestamps))
	for _, ts := range userTimestamps {
		if ts.After(limitWindowStart) {
			validTimestamps = append(validTimestamps, ts)
		}
	}

	// Проверяем количество меток
	limitExceeded := len(validTimestamps) >= limitCount

	if b.config.Debug {
		log.Printf("[Direct Limit Check] Chat %d, User %d: Enabled=%t, Count=%d, Duration=%v. Timestamps in window: %d. Limit exceeded: %t",
			chatID, userID, enabled, limitCount, limitDuration, len(validTimestamps), limitExceeded)
	}

	// Обновляем метки пользователя ТОЛЬКО если лимит НЕ превышен
	if !limitExceeded {
		validTimestamps = append(validTimestamps, now)
		b.directReplyTimestamps[chatID][userID] = validTimestamps // Теперь запись под Lock() безопасна
		if b.config.Debug {
			log.Printf("[Direct Limit Check] Chat %d, User %d: Timestamp added. Total in window now: %d", chatID, userID, len(validTimestamps))
		}
	} else {
		if b.config.Debug {
			log.Printf("[Direct Limit Check] Chat %d, User %d: Limit exceeded, timestamp NOT added.", chatID, userID)
		}
		// Очищаем старые метки, даже если лимит превышен, чтобы не накапливать мусор
		b.directReplyTimestamps[chatID][userID] = validTimestamps
	}

	return limitExceeded // Возвращаем true если лимит ПРЕВЫШЕН
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
