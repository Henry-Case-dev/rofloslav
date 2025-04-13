package bot

import (
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Максимальное количество последних сообщений из 24ч окна для отправки на саммари
const maxMessagesForSummary = 1500

const telegramMaxMessageLength = 4096

// createAndSendSummary создает и отправляет саммари диалога,
// редактируя существующее сообщение или отправляя новое.
func (b *Bot) createAndSendSummary(chatID int64) {
	// Получаем ID сообщения "Генерирую..."
	b.settingsMutex.RLock()
	settings, exists := b.chatSettings[chatID]
	var infoMessageID int
	if exists {
		infoMessageID = settings.LastInfoMessageID
	} else {
		log.Printf("[WARN][createAndSendSummary] Чат %d: Настройки не найдены, не могу получить LastInfoMessageID.", chatID)
	}
	b.settingsMutex.RUnlock()

	// --- Сообщение для редактирования/отправки ---
	var editText, sendText string // Текст для редактирования или нового сообщения
	parseMode := ""               // Режим парсинга (Markdown)

	// Получаем сообщения за последние 24 часа
	messages := b.storage.GetMessagesSince(chatID, time.Now().Add(-24*time.Hour))
	if len(messages) == 0 {
		editText = "Недостаточно сообщений за последние 24 часа для создания саммари."
		sendText = editText
		b.updateOrCreateMessage(chatID, infoMessageID, editText, sendText, parseMode)
		return
	}

	// --- Ограничение количества сообщений для саммари ---
	if len(messages) > maxMessagesForSummary {
		log.Printf("[DEBUG][Summary] Чат %d: Слишком много сообщений для саммари (%d > %d). Обрезаю до последних %d.", chatID, len(messages), maxMessagesForSummary, maxMessagesForSummary)
		messages = messages[len(messages)-maxMessagesForSummary:]
	}
	// --- Конец ограничения ---

	if b.config.Debug {
		log.Printf("[DEBUG] Создаю саммари для чата %d. Используется сообщений: %d. Инфо-сообщение ID: %d", chatID, len(messages), infoMessageID)
	}

	// Используем только промпт для саммари без комбинирования
	summaryPrompt := b.config.SummaryPrompt

	// --- Формирование контекста с профилями ---
	// Передаем cfg и llmClient для возможного использования долгосрочной памяти при генерации саммари (хотя пока не используется)
	contextText := formatHistoryWithProfiles(chatID, messages, b.storage, b.config, b.llm, b.config.Debug, b.config.TimeZone)
	if contextText == "" {
		log.Printf("[WARN][createAndSendSummary] Чат %d: Отформатированный контекст для саммари пуст.", chatID)
		editText = "Не удалось подготовить данные для саммари (контекст пуст)."
		sendText = editText
		b.updateOrCreateMessage(chatID, infoMessageID, editText, sendText, parseMode)
		return
	}
	// --- Конец форматирования ---

	const maxAttempts = 3 // Максимальное количество попыток генерации
	const minWords = 10   // Минимальное количество слов в саммари

	var finalSummary string
	var lastErr error // Сохраняем последнюю ошибку API
	var attempt int

	for attempt = 1; attempt <= maxAttempts; attempt++ {
		if b.config.Debug {
			log.Printf("[DEBUG] Чат %d: Попытка генерации саммари №%d", chatID, attempt)
		}

		// Отправляем запрос к LLM с промптом для саммари и собранным контекстом
		summary, err := b.llm.GenerateArbitraryResponse(summaryPrompt, contextText)
		if err != nil {
			lastErr = err // Сохраняем последнюю ошибку
			if b.config.Debug {
				log.Printf("[DEBUG] Чат %d: Ошибка при генерации саммари (попытка %d): %v", chatID, attempt, err)
			}
			if attempt < maxAttempts {
				time.Sleep(1 * time.Second)
			}
			continue // Переходим к следующей попытке
		}

		// Проверяем количество слов
		wordCount := len(strings.Fields(summary))
		if b.config.Debug {
			log.Printf("[DEBUG] Чат %d: Сгенерировано саммари (попытка %d), слов: %d.", chatID, attempt, wordCount)
		}

		if wordCount >= minWords {
			finalSummary = summary
			lastErr = nil // Сбрасываем ошибку при успехе
			break         // Успешная генерация, выходим из цикла
		}

		// Если слов мало, добавляем небольшую задержку перед следующей попыткой
		if attempt < maxAttempts {
			time.Sleep(1 * time.Second)
		}
	}

	// Формируем текст ответа в зависимости от результата
	if finalSummary != "" {
		if b.config.Debug {
			log.Printf("[DEBUG] Саммари успешно создано для чата %d после %d попыток", chatID, attempt)
		}
		// --- Логируем текст до и после экранирования ---
		if b.config.Debug {
			log.Printf("[DEBUG][Summary Raw] Chat %d: \n---START RAW---\n%s\n---END RAW---", chatID, finalSummary)
		}
		// Экранируем Markdown V2 перед отправкой
		escapedSummary := escapeMarkdownV2(finalSummary)
		if b.config.Debug {
			log.Printf("[DEBUG][Summary Escaped] Chat %d: \n---START ESCAPED---\n%s\n---END ESCAPED---", chatID, escapedSummary)
		}
		// --- Конец логирования ---

		// Устанавливаем текст и ParseMode для успешного саммари
		editText = fmt.Sprintf("📋 Саммари диалога за последние 24 часа:\n\n%s", escapedSummary)
		sendText = editText
		parseMode = "MarkdownV2" // <--- Устанавливаем правильный ParseMode!
	} else {
		if b.config.Debug {
			log.Printf("[DEBUG] Чат %d: Не удалось сгенерировать достаточно длинное саммари после %d попыток.", chatID, maxAttempts)
		}
		errMsg := "Не удалось создать достаточно информативное саммари после нескольких попыток."
		if lastErr != nil {
			errMsg += fmt.Sprintf(" Последняя ошибка: %v", lastErr)
		}
		editText = errMsg
		sendText = editText
		parseMode = "" // Ошибки без форматирования
	}

	// Обновляем или создаем сообщение
	b.updateOrCreateMessage(chatID, infoMessageID, editText, sendText, parseMode)
}

// updateOrCreateMessage редактирует существующее сообщение или отправляет новое.
// Если текст слишком длинный, разбивает его на части.
func (b *Bot) updateOrCreateMessage(chatID int64, messageID int, editText, sendText, parseMode string) {
	textToSend := sendText // Текст для нового сообщения
	if messageID != 0 {
		textToSend = editText // Текст для редактирования, если есть ID
	}

	// Проверяем длину текста
	if len([]rune(textToSend)) <= telegramMaxMessageLength {
		// Текст в пределах лимита, отправляем/редактируем как обычно
		b.sendOrEditSingleMessage(chatID, messageID, editText, sendText, parseMode)
		return
	}

	// Текст слишком длинный, нужно разбивать
	if b.config.Debug {
		log.Printf("[DEBUG][SplitMsg] Chat %d: Текст превышает лимит (%d > %d). Начинаю разбивку.", chatID, len([]rune(textToSend)), telegramMaxMessageLength)
	}

	chunks := splitMessageIntoChunks(textToSend, telegramMaxMessageLength)

	// Отправляем или редактируем первую часть
	if messageID != 0 {
		editMsg := tgbotapi.NewEditMessageText(chatID, messageID, chunks[0])
		if parseMode != "" {
			editMsg.ParseMode = parseMode
		}
		if b.config.Debug {
			log.Printf("[DEBUG][SplitMsg Edit] Chat %d, MsgID %d: Попытка редактирования первой части. ParseMode: '%s'", chatID, messageID, editMsg.ParseMode)
		}
		_, err := b.api.Send(editMsg)
		if err != nil {
			// Если редактирование не удалось, отправляем первую часть как новое сообщение
			log.Printf("[WARN][SplitMsg Edit] Chat %d, MsgID %d: Не удалось отредактировать: %v. Отправляю первую часть новым сообщением.", chatID, messageID, err)
			b.sendSingleMessage(chatID, chunks[0], parseMode) // Отправляем первую часть
		}
	} else {
		// Если исходного сообщения для редактирования не было, отправляем первую часть
		b.sendSingleMessage(chatID, chunks[0], parseMode)
	}

	// Отправляем остальные части как новые сообщения
	for i := 1; i < len(chunks); i++ {
		time.Sleep(500 * time.Millisecond) // Небольшая задержка между частями
		b.sendSingleMessage(chatID, chunks[i], parseMode)
	}

	// Обновляем LastInfoMessageID на ID *последнего* отправленного сообщения (если чанки были)
	// Это не совсем точно, т.к. последнее отправленное сообщение будет только частью саммари.
	// Пока оставим так, т.к. непонятно, ID какого сообщения важнее сохранять.
	// Возможно, стоит вообще не обновлять LastInfoMessageID при разбивке.
	/*
		lastSentMsgID := b.getLastSentMessageIDSomehow() // <--- Нужен механизм получения ID последнего сообщения
		if lastSentMsgID != 0 {
			b.settingsMutex.Lock()
			if settings, exists := b.chatSettings[chatID]; exists {
				settings.LastInfoMessageID = lastSentMsgID
				log.Printf("[DEBUG][SplitMsg] Сохранен ID последнего чанка: %d для чата %d", lastSentMsgID, chatID)
			} else {
				log.Printf("[WARN][SplitMsg] Настройки для чата %d не найдены при попытке сохранить ID последнего чанка.", chatID)
			}
			b.settingsMutex.Unlock()
		}
	*/
}

// sendOrEditSingleMessage отправляет или редактирует одно сообщение в пределах лимита
func (b *Bot) sendOrEditSingleMessage(chatID int64, messageID int, editText, sendText, parseMode string) {
	if messageID != 0 {
		// Пытаемся отредактировать существующее сообщение
		editMsg := tgbotapi.NewEditMessageText(chatID, messageID, editText)
		if parseMode != "" {
			editMsg.ParseMode = parseMode
		}
		// Добавляем логирование перед отправкой редактирования
		if b.config.Debug {
			log.Printf("[DEBUG][UpdateMsg Edit] Chat %d, MsgID %d: Attempting to edit. ParseMode: '%s'", chatID, messageID, editMsg.ParseMode)
		}
		_, err := b.api.Send(editMsg)
		if err == nil {
			if b.config.Debug {
				log.Printf("[DEBUG][UpdateMsg] Сообщение %d в чате %d успешно отредактировано.", messageID, chatID)
			}
			// Успешно отредактировано, выходим (LastInfoMessageID не меняется)
			return
		}
		// Если редактирование не удалось (например, сообщение удалено), логгируем и отправляем новое
		log.Printf("[WARN][UpdateMsg] Не удалось отредактировать сообщение %d в чате %d: %v. Отправляю новое.", messageID, chatID, err)
		// ID старого сообщения больше не актуален, сбрасываем его в настройках
		b.settingsMutex.Lock()
		if settings, exists := b.chatSettings[chatID]; exists {
			if settings.LastInfoMessageID == messageID {
				settings.LastInfoMessageID = 0
			}
		}
		b.settingsMutex.Unlock()
	} else {
		if b.config.Debug {
			log.Printf("[DEBUG][UpdateMsg] MessageID == 0 для чата %d. Отправляю новое сообщение.", chatID)
		}
	}

	// Отправляем новое сообщение (если редактирование не удалось или messageID был 0)
	b.sendSingleMessage(chatID, sendText, parseMode)
}

// sendSingleMessage отправляет одно сообщение и обновляет LastInfoMessageID
func (b *Bot) sendSingleMessage(chatID int64, text string, parseMode string) {
	msg := tgbotapi.NewMessage(chatID, text)
	if parseMode != "" {
		msg.ParseMode = parseMode
	}
	// Добавляем логирование перед отправкой нового сообщения
	if b.config.Debug {
		log.Printf("[DEBUG][UpdateMsg Send] Chat %d: Attempting to send new. ParseMode: '%s', Length: %d", chatID, msg.ParseMode, len([]rune(text)))
	}
	sentMsg, err := b.api.Send(msg)
	if err != nil {
		log.Printf("[ERROR][UpdateMsg] Ошибка отправки нового сообщения в чат %d: %v", chatID, err)
		return // Если не удалось отправить, ID не сохраняем
	}

	// Сохраняем ID нового сообщения
	b.settingsMutex.Lock()
	if settings, exists := b.chatSettings[chatID]; exists {
		settings.LastInfoMessageID = sentMsg.MessageID
		if b.config.Debug {
			log.Printf("[DEBUG][UpdateMsg] Сохранен новый LastInfoMessageID: %d для чата %d", sentMsg.MessageID, chatID)
		}
	} else {
		log.Printf("[WARN][UpdateMsg] Настройки для чата %d не найдены при попытке сохранить новый LastInfoMessageID.", chatID)
	}
	b.settingsMutex.Unlock()
}

// splitMessageIntoChunks разбивает длинный текст на части, подходящие для Telegram
func splitMessageIntoChunks(text string, maxLength int) []string {
	var chunks []string
	runes := []rune(text)
	textLength := len(runes)
	start := 0

	for start < textLength {
		// Определяем конец текущего потенциального чанка
		effectiveEnd := start + maxLength
		if effectiveEnd > textLength {
			effectiveEnd = textLength
		}
		// Копируем исходное значение end, которое будем корректировать
		end := effectiveEnd

		// Определяем, с какого места искать разделители (например, последняя четверть)
		searchStart := start + (maxLength * 3 / 4)
		// Корректируем searchStart, если он выходит за пределы effectiveEnd
		if searchStart >= effectiveEnd || searchStart < start { // Добавил проверку searchStart < start на всякий случай
			searchStart = start // Если чанк короткий, ищем с самого начала
		}

		// Проверяем, есть ли вообще что искать (если searchStart == effectiveEnd)
		if searchStart >= effectiveEnd {
			// Нечего искать, просто берем весь чанк до effectiveEnd
			chunks = append(chunks, string(runes[start:effectiveEnd]))
			start = effectiveEnd
			continue // Переходим к следующему чанку
		}

		// Объявляем переменные для хранения позиций разделителей
		lastDoubleNewline := -1
		lastNewline := -1
		lastPeriod := -1
		bestSplit := -1 // Также инициализируем bestSplit

		tempRunes := runes[searchStart:effectiveEnd] // Используем effectiveEnd

		// Ищем "\n\n"
		indicesDouble := findAllIndices(tempRunes, []rune("\n\n")) // Используем :=
		if len(indicesDouble) > 0 {
			// Нашли "\n\n", берем последнее вхождение в диапазоне [searchStart, effectiveEnd)
			lastDoubleNewline = searchStart + indicesDouble[len(indicesDouble)-1] // Присваивание без :=
			bestSplit = lastDoubleNewline                                         // Запоминаем позицию *начала* "\n\n"
		}

		// Ищем "\n" (только если не нашли "\n\n")
		if bestSplit == -1 { // Используем обновленный bestSplit
			indicesSingle := findAllIndices(tempRunes, []rune("\n"))
			if len(indicesSingle) > 0 {
				lastNewline = searchStart + indicesSingle[len(indicesSingle)-1] // Присваивание без :=
				bestSplit = lastNewline                                         // Запоминаем позицию *начала* "\n"
			}
		}

		// Ищем "." (только если не нашли ни "\n\n", ни "\n")
		if bestSplit == -1 { // Используем обновленный bestSplit
			indicesPeriod := findAllIndices(tempRunes, []rune("."))
			if len(indicesPeriod) > 0 {
				lastPeriod = searchStart + indicesPeriod[len(indicesPeriod)-1] // Присваивание без :=
				bestSplit = lastPeriod                                         // Запоминаем позицию "."
			}
		}

		// Если нашли какой-то разделитель
		if bestSplit != -1 {
			// Определяем, где закончится чанк
			splitLen := 1 // Длина разделителя по умолчанию (для '.' или '\n')
			if lastDoubleNewline != -1 && bestSplit == lastDoubleNewline {
				splitLen = 2 // Длина "\n\n"
			} else if lastNewline != -1 && bestSplit == lastNewline {
				splitLen = 1 // Длина "\n"
			} else if lastPeriod != -1 && bestSplit == lastPeriod {
				splitLen = 1 // Длина "."
			}
			end = bestSplit + splitLen // Заканчиваем чанк *после* разделителя
		} else {
			// Разделителей не найдено в диапазоне, просто режем по maxLength
			// end уже равен effectiveEnd, который был вычислен в начале
			end = effectiveEnd // Используем уже вычисленный effectiveEnd
		}

		// Добавляем найденный чанк
		chunks = append(chunks, string(runes[start:end]))
		start = end
	}

	return chunks
}

// findAllIndices находит все вхождения подстроки (как []rune) в тексте (как []rune)
// Возвращает слайс индексов начала каждого вхождения.
func findAllIndices(text, sub []rune) []int {
	var indices []int
	textLen := len(text)
	subLen := len(sub)
	if subLen == 0 || subLen > textLen {
		return indices
	}
	for i := 0; i <= textLen-subLen; i++ {
		match := true
		for j := 0; j < subLen; j++ {
			if text[i+j] != sub[j] {
				match = false
				break
			}
		}
		if match {
			indices = append(indices, i)
			i += subLen - 1 // Перескакиваем найденное вхождение, чтобы не находить перекрывающиеся
		}
	}
	return indices
}
