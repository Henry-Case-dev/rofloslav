package bot

import (
	"fmt"
	"log"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// toggleSrachAnalysis переключает состояние анализа срачей для чата
func (b *Bot) toggleSrachAnalysis(chatID int64) (bool, error) {
	// 1. Получаем текущие настройки из БД
	dbSettings, err := b.storage.GetChatSettings(chatID)
	if err != nil {
		log.Printf("[ERROR][toggleSrachAnalysis] Chat %d: Не удалось получить настройки: %v", chatID, err)
		return false, err
	}

	// 2. Определяем текущее и новое состояние
	currentEnabled := b.config.SrachAnalysisEnabled // Дефолт из конфига
	if dbSettings.SrachAnalysisEnabled != nil {
		currentEnabled = *dbSettings.SrachAnalysisEnabled
	}
	newEnabled := !currentEnabled

	// 3. Обновляем настройку в хранилище
	errUpdate := b.storage.UpdateSrachAnalysisEnabled(chatID, newEnabled)
	if errUpdate != nil {
		log.Printf("[ERROR][toggleSrachAnalysis] Chat %d: Не удалось обновить настройку: %v", chatID, errUpdate)
		return currentEnabled, errUpdate // Возвращаем старое значение и ошибку
	}

	log.Printf("Чат %d: Анализ срачей переключен на %s", chatID, getEnabledStatusText(newEnabled))

	// 4. Сбрасываем состояние срача в памяти, если настройка была изменена
	b.settingsMutex.Lock()
	if settings, exists := b.chatSettings[chatID]; exists {
		settings.SrachAnalysisEnabled = newEnabled // Обновляем и в памяти для консистентности
		settings.SrachState = "none"
		settings.SrachMessages = nil
	}
	b.settingsMutex.Unlock()

	return newEnabled, nil
}

// sendSrachWarning отправляет предупреждение о начале срача
func (b *Bot) sendSrachWarning(chatID int64) {
	// Используем только SRACH_WARNING_PROMPT
	warningPrompt := b.config.SRACH_WARNING_PROMPT

	// Генерируем кастомное предупреждение с помощью LLM
	warningText, err := b.llm.GenerateArbitraryResponse(warningPrompt, "")
	if err != nil {
		log.Printf("Ошибка генерации предупреждения о сраче: %v", err)
		warningText = warningPrompt // Используем промпт как запасной вариант
	}

	b.sendReply(chatID, "🚨🚨🚨 "+warningText)
}

// analyseSrach анализирует завершенный срач
func (b *Bot) analyseSrach(chatID int64) {
	b.settingsMutex.Lock()
	settings, exists := b.chatSettings[chatID]
	if !exists || settings.SrachState != "detected" || len(settings.SrachMessages) == 0 {
		log.Printf("analyseSrach: Нет данных для анализа срача в чате %d (State: %s, Msgs: %d)",
			chatID, settings.SrachState, len(settings.SrachMessages))
		b.settingsMutex.Unlock()
		return
	}

	// Копируем сообщения и меняем состояние перед разблокировкой
	srachMessagesToAnalyze := make([]string, len(settings.SrachMessages))
	copy(srachMessagesToAnalyze, settings.SrachMessages)
	settings.SrachState = "analyzing" // Устанавливаем состояние анализа
	b.settingsMutex.Unlock()          // Разблокируем перед длительной операцией LLM

	log.Printf("Начинаю анализ срача в чате %d (%d сообщений)", chatID, len(srachMessagesToAnalyze))

	// Формируем контекст для LLM
	contextText := strings.Join(srachMessagesToAnalyze, "\n---\n")
	analysisPrompt := b.config.SRACH_ANALYSIS_PROMPT

	// Генерируем анализ
	analysis, err := b.llm.GenerateArbitraryResponse(analysisPrompt, contextText)

	// Обновляем состояние после анализа
	b.settingsMutex.Lock()
	if settings, exists := b.chatSettings[chatID]; exists { // Проверяем повторно
		settings.SrachState = "none" // Сбрасываем состояние после анализа
		settings.SrachMessages = nil // Очищаем сообщения срача
		settings.SrachStartTime = time.Time{}
		settings.LastSrachTriggerTime = time.Time{}
	} else {
		log.Printf("analyseSrach: Настройки для чата %d исчезли во время анализа!", chatID)
	}
	b.settingsMutex.Unlock()

	if err != nil {
		log.Printf("Ошибка анализа срача в чате %d: %v", chatID, err)
		b.sendReply(chatID, "Не удалось проанализировать недавний спор. Возможно, он был слишком хорош для моего понимания.")
		return
	}

	log.Printf("Анализ срача в чате %d завершен.", chatID)
	b.sendReply(chatID, "🧐 *Разбор полетов:*\n\n"+analysis)
}

// isPotentialSrachTrigger проверяет, является ли сообщение потенциальным триггером срача
func (b *Bot) isPotentialSrachTrigger(message *tgbotapi.Message) bool {
	if message == nil || message.Text == "" {
		return false
	}

	textLower := strings.ToLower(message.Text)

	// 1. Проверка по ключевым словам
	for _, keyword := range b.config.SrachKeywords {
		if strings.Contains(textLower, keyword) {
			log.Printf("[Srach Trigger] Чат %d: Найдено ключевое слово '%s' в сообщении ID %d", message.Chat.ID, keyword, message.MessageID)
			return true
		}
	}

	// 2. Проверка на ответ/упоминание в агрессивной манере (TODO: Улучшить)
	if message.ReplyToMessage != nil {
		// Простая проверка на наличие негативных слов в ответе (очень грубо)
		negativeWords := []string{"нет, ты", "сам такой", "бред", "чушь", "глупость", "ошибаешься"}
		for _, word := range negativeWords {
			if strings.Contains(textLower, word) {
				log.Printf("[Srach Trigger] Чат %d: Потенциально агрессивный ответ на сообщение ID %d", message.Chat.ID, message.ReplyToMessage.MessageID)
				return true
			}
		}
	}

	// TODO: Добавить другие эвристики (например, частота сообщений, длина, капс)

	return false
}

// confirmSrachWithLLM использует LLM для подтверждения, является ли сообщение частью срача
func (b *Bot) confirmSrachWithLLM(chatID int64, messageText string) bool {
	prompt := b.config.SRACH_CONFIRM_PROMPT

	// Добавляем текст сообщения к промпту
	fullPrompt := fmt.Sprintf("%s\n\n%s", prompt, messageText)

	// Используем GenerateArbitraryResponse, так как нам не нужен сложный контекст/история
	response, err := b.llm.GenerateArbitraryResponse(fullPrompt, "") // Контекст пустой
	if err != nil {
		log.Printf("Ошибка LLM при подтверждении срача в чате %d: %v", chatID, err)
		return false // В случае ошибки считаем, что не срач
	}

	// Интерпретируем ответ LLM (ожидаем 'true' или 'false')
	result := strings.TrimSpace(strings.ToLower(response))
	if b.config.Debug {
		log.Printf("[LLM Srach Confirm] Чат %d: Ответ LLM на подтверждение: '%s' (интерпретировано как %t)",
			chatID, result, result == "true")
	}
	return result == "true"
}
