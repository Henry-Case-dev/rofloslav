package bot

import (
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/Henry-Case-dev/rofloslav/internal/storage"
	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Максимальное количество последних сообщений из 24ч окна для отправки на саммари
const maxMessagesForSummary = 1500

const telegramMaxMessageLength = 4096

// createAndSendSummary создает и отправляет саммари диалога,
// редактируя существующее сообщение или отправляя новое.
func (b *Bot) createAndSendSummary(chatID int64) {
	if b.config.Debug {
		log.Printf("[DEBUG][createAndSendSummary] Чат %d: Функция вызвана.", chatID)
	}

	// Объявляем переменные для текста сообщения
	var editText, sendText string // Текст для редактирования или нового сообщения
	parseMode := ""               // Режим парсинга (Markdown)

	// Загружаем или берем из кэша настройки чата
	b.settingsMutex.RLock()
	chatSettings, exists := b.chatSettings[chatID]
	b.settingsMutex.RUnlock()
	if !exists {
		log.Printf("[Summary] Ошибка: Настройки для чата %d не найдены.", chatID)
		return
	}

	// Получаем сообщения за последние 24 часа
	messages, errMsgs := b.storage.GetMessagesSince(chatID, time.Now().Add(-24*time.Hour))
	if errMsgs != nil {
		log.Printf("[Summary] Chat %d: Ошибка получения сообщений: %v", chatID, errMsgs)
		// Обновляем сообщение, если есть ID
		editText = fmt.Sprintf("❌ Ошибка при получении сообщений для саммари: %v", errMsgs)
		sendText = editText // Используем тот же текст для отправки нового сообщения
		b.updateOrCreateMessage(chatID, chatSettings.LastInfoMessageID, editText, sendText, "")
		return
	}

	if len(messages) == 0 {
		editText = "Недостаточно сообщений за последние 24 часа для создания саммари."
		sendText = editText
		b.updateOrCreateMessage(chatID, chatSettings.LastInfoMessageID, editText, sendText, "")
		return
	}

	// --- Получение профилей пользователей ---
	userProfiles, errProfiles := b.storage.GetAllUserProfiles(chatID)
	if errProfiles != nil {
		log.Printf("[Summary] Chat %d: Ошибка получения профилей пользователей: %v", chatID, errProfiles)
		// Можно продолжить без профилей или вернуть ошибку?
		// Пока продолжим, просто залогировав.
		userProfiles = []*storage.UserProfile{} // Используем пустой срез
	}

	// --- Форматирование истории с профилями ---
	// loc, _ := time.LoadLocation(b.config.TimeZone) // Загружаем таймзону (перенесено внутрь formatHistoryWithProfiles)
	contextText := formatHistoryWithProfiles(chatID, messages, b.storage, b.config, b.llm, b.config.Debug, b.config.TimeZone)

	if contextText == "" {
		log.Printf("[Summary] Chat %d: Контекст для саммари пуст после форматирования.", chatID)
		editText = "Не удалось подготовить данные для саммари (контекст пуст)."
		sendText = editText
		b.updateOrCreateMessage(chatID, chatSettings.LastInfoMessageID, editText, sendText, "")
		return
	}

	if b.config.Debug {
		log.Printf("[DEBUG] Создаю саммари для чата %d. Используется сообщений: %d (%d профилей). Инфо-сообщение ID: %d",
			chatID, len(messages), len(userProfiles), chatSettings.LastInfoMessageID) // Используем userProfiles в логе
	}

	// --- Генерация саммари ---

	// --- Ограничение количества сообщений для саммари ---
	if len(messages) > maxMessagesForSummary {
		log.Printf("[DEBUG][Summary] Чат %d: Слишком много сообщений для саммари (%d > %d). Обрезаю до последних %d.", chatID, len(messages), maxMessagesForSummary, maxMessagesForSummary)
		messages = messages[len(messages)-maxMessagesForSummary:]
	}
	// --- Конец ограничения ---

	// Используем только промпт для саммари без комбинирования
	summaryPrompt := b.config.SummaryPrompt

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
	b.updateOrCreateMessage(chatID, chatSettings.LastInfoMessageID, editText, sendText, parseMode)
}

// updateOrCreateMessage редактирует существующее сообщение или отправляет новое.
// Если текст слишком длинный, разбивает его на части.
func (b *Bot) updateOrCreateMessage(chatID int64, messageID int, editText, sendText, parseMode string) {
	if messageID != 0 {
		// Попытка редактирования
		editMsg := tgbotapi.NewEditMessageText(chatID, messageID, editText)
		// Устанавливаем ParseMode, если он передан
		if parseMode != "" {
			editMsg.ParseMode = parseMode
		}
		_, err := b.api.Send(editMsg)
		if err == nil {
			// Успешно отредактировано
			log.Printf("[DEBUG][Summary] Сообщение саммари (ID: %d) в чате %d успешно отредактировано.", messageID, chatID)
			return
		}
		// Если редактирование не удалось (например, сообщение слишком старое или удалено)
		log.Printf("[WARN][Summary] Не удалось отредактировать сообщение саммари (ID: %d) в чате %d: %v. Отправляю новое.", messageID, chatID, err)
		// Сбрасываем messageID в настройках, чтобы следующее саммари создало новое сообщение
		b.settingsMutex.Lock()
		if settings, exists := b.chatSettings[chatID]; exists {
			if settings.LastInfoMessageID == messageID { // Убедимся, что это было именно то сообщение
				settings.LastInfoMessageID = 0
			}
		}
		b.settingsMutex.Unlock()
	}

	// Отправка нового сообщения
	msg := tgbotapi.NewMessage(chatID, sendText)
	// Устанавливаем ParseMode, если он передан
	if parseMode != "" {
		msg.ParseMode = parseMode
	}
	sentMsg, err := b.api.Send(msg)
	if err != nil {
		log.Printf("[ERROR][Summary] Не удалось отправить новое сообщение саммари в чат %d: %v", chatID, err)
		return
	}
	log.Printf("[DEBUG][Summary] Новое сообщение саммари (ID: %d) отправлено в чат %d.", sentMsg.MessageID, chatID)

	// Сохраняем ID нового сообщения
	b.settingsMutex.Lock()
	if settings, exists := b.chatSettings[chatID]; exists {
		settings.LastInfoMessageID = sentMsg.MessageID
	}
	b.settingsMutex.Unlock()
}

// sendOrEditSingleMessage отправляет или редактирует одно сообщение в пределах лимита
func (b *Bot) sendOrEditSingleMessage(chatID int64, messageID int, editText, sendText, parseMode string) {
	if messageID > 0 {
		editMsg := tgbotapi.NewEditMessageText(chatID, messageID, editText)
		if parseMode != "" {
			editMsg.ParseMode = parseMode
		}
		_, err := b.api.Send(editMsg)
		if err == nil {
			return // Успешно отредактировано
		}
		log.Printf("[WARN] Не удалось отредактировать сообщение (ID: %d) в чате %d: %v. Отправляю новое.", messageID, chatID, err)
	}

	// Отправляем новое, если редактирование не удалось или messageID == 0
	msg := tgbotapi.NewMessage(chatID, sendText)
	if parseMode != "" {
		msg.ParseMode = parseMode
	}
	_, err := b.api.Send(msg)
	if err != nil {
		log.Printf("[ERROR] Не удалось отправить сообщение в чат %d: %v", chatID, err)
	}
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
